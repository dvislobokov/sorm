// Package migrate — декларативные миграции из кода приложения на движке
// Atlas (ariga.io/atlas как Go-зависимость, без внешнего CLI):
//
//	sdb, _ := sql.Open("pgx", dsn) // database/sql-соединение
//	err := migrate.Apply(ctx, sdb, "postgres")
//
// Желаемая схема берётся из sorm.Tables() (регистрируется сгенерированным
// пакетом sormgen), текущая — инспекцией БД; Atlas вычисляет и применяет
// дифф. Plan возвращает SQL без применения (dry-run / ревью).
//
// Рантайм sorm НЕ зависит от Atlas: зависимость линкуется только при
// импорте этого пакета. Для версионных файловых миграций (CI, ревью,
// прод) используйте `sorm schema` + atlas CLI — см. docs/design.md.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	atlasmigrate "ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/mysql"
	"ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"

	"sorm"
)

// Apply приводит схему БД к состоянию зарегистрированных моделей.
// Сравнение ограничено таблицами sorm — чужие таблицы не трогаются.
// Конкурентные вызовы сериализуются advisory lock'ом.
func Apply(ctx context.Context, db *sql.DB, dialect string) error {
	return withMigrationLock(ctx, db, dialect, func() error {
		drv, changes, err := diff(ctx, db, dialect)
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			return nil
		}
		if err := drv.ApplyChanges(ctx, changes); err != nil {
			return fmt.Errorf("sorm/migrate: apply: %w", err)
		}
		return nil
	})
}

// Plan возвращает SQL-статименты диффа без применения.
func Plan(ctx context.Context, db *sql.DB, dialect string) ([]string, error) {
	drv, changes, err := diff(ctx, db, dialect)
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return nil, nil
	}
	plan, err := drv.PlanChanges(ctx, "sorm-diff", changes)
	if err != nil {
		return nil, fmt.Errorf("sorm/migrate: plan: %w", err)
	}
	out := make([]string, len(plan.Changes))
	for i, c := range plan.Changes {
		out[i] = c.Cmd
	}
	return out, nil
}

func diff(ctx context.Context, db *sql.DB, dialect string) (atlasmigrate.Driver, []schema.Change, error) {
	drv, err := open(db, dialect)
	if err != nil {
		return nil, nil, err
	}

	defs := sorm.Tables()
	if len(defs) == 0 {
		return nil, nil, fmt.Errorf("sorm/migrate: no tables registered — import your generated sormgen package")
	}
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}

	// Инспектируем только таблицы sorm: дифф не предложит DROP чужого.
	current, err := drv.InspectSchema(ctx, "", &schema.InspectOptions{Tables: names})
	if err != nil {
		return nil, nil, fmt.Errorf("sorm/migrate: inspect: %w", err)
	}

	desired, err := buildSchema(current.Name, defs, dialect)
	if err != nil {
		return nil, nil, err
	}

	changes, err := drv.SchemaDiff(current, desired)
	if err != nil {
		return nil, nil, fmt.Errorf("sorm/migrate: diff: %w", err)
	}
	return drv, changes, nil
}

func open(db *sql.DB, dialect string) (atlasmigrate.Driver, error) {
	switch dialect {
	case "postgres":
		return postgres.Open(db)
	case "mysql":
		return mysql.Open(db)
	case "sqlite":
		return sqlite.Open(db)
	default:
		return nil, fmt.Errorf("sorm/migrate: unknown dialect %q (postgres|mysql|sqlite)", dialect)
	}
}

// buildSchema строит желаемую схему Atlas из sorm.TableDef.
func buildSchema(name string, defs []sorm.TableDef, dialect string) (*schema.Schema, error) {
	s := schema.New(name)
	tables := map[string]*schema.Table{}

	// Первый проход: таблицы и колонки.
	for _, def := range defs {
		t := schema.NewTable(def.Name)
		var pkCols []*schema.Column
		for _, c := range def.Columns {
			col, err := buildColumn(c, dialect)
			if err != nil {
				return nil, fmt.Errorf("sorm/migrate: %s.%s: %w", def.Name, c.Name, err)
			}
			t.AddColumns(col)
			if c.PK {
				pkCols = append(pkCols, col) // композитный PK поддержан
			}
			if c.Unique {
				t.AddIndexes(schema.NewUniqueIndex(def.Name + "_" + c.Name + "_key").AddColumns(col))
			}
		}
		if len(pkCols) > 0 {
			t.SetPrimaryKey(schema.NewPrimaryKey(pkCols...))
		}
		for _, ix := range def.Indexes {
			sx, err := buildIndex(t, def.Name, ix, dialect)
			if err != nil {
				return nil, err
			}
			t.AddIndexes(sx)
		}
		tables[def.Name] = t
		s.AddTables(t)
	}

	// Второй проход: FK (обе таблицы уже существуют).
	for _, def := range defs {
		t := tables[def.Name]
		for _, c := range def.Columns {
			if c.RefTable == "" {
				continue
			}
			ref, ok := tables[c.RefTable]
			if !ok {
				return nil, fmt.Errorf("sorm/migrate: %s.%s references unknown table %s", def.Name, c.Name, c.RefTable)
			}
			refCol, ok := columnOf(ref, c.RefCol)
			if !ok {
				return nil, fmt.Errorf("sorm/migrate: %s.%s references unknown column %s.%s", def.Name, c.Name, c.RefTable, c.RefCol)
			}
			ownCol, _ := columnOf(t, c.Name)
			t.AddForeignKeys(
				schema.NewForeignKey(def.Name + "_" + c.Name + "_fkey").
					AddColumns(ownCol).
					SetRefTable(ref).
					AddRefColumns(refCol),
			)
		}
	}
	return s, nil
}

func buildIndex(t *schema.Table, table string, ix sorm.IndexDef, dialect string) (*schema.Index, error) {
	var sx *schema.Index
	if ix.Unique {
		sx = schema.NewUniqueIndex(ix.Name)
	} else {
		sx = schema.NewIndex(ix.Name)
	}
	for _, part := range ix.IndexParts() {
		var p *schema.IndexPart
		switch {
		case part.Expr != "":
			p = schema.NewExprPart(&schema.RawExpr{X: part.Expr})
		default:
			col, ok := columnOf(t, part.Column)
			if !ok {
				return nil, fmt.Errorf("sorm/migrate: index %s: unknown column %s.%s", ix.Name, table, part.Column)
			}
			p = schema.NewColumnPart(col)
		}
		sx.AddParts(p.SetDesc(part.Desc))
	}
	if ix.Type != "" {
		switch dialect {
		case "postgres":
			sx.AddAttrs(&postgres.IndexType{T: strings.ToUpper(ix.Type)})
		case "mysql":
			sx.AddAttrs(&mysql.IndexType{T: strings.ToUpper(ix.Type)})
		default:
			return nil, fmt.Errorf("sorm/migrate: index %s: тип индекса не поддерживается на %s", ix.Name, dialect)
		}
	}
	if ix.Where != "" {
		switch dialect {
		case "postgres":
			sx.AddAttrs(&postgres.IndexPredicate{P: ix.Where})
		case "sqlite":
			sx.AddAttrs(&sqlite.IndexPredicate{P: ix.Where})
		default:
			return nil, fmt.Errorf("sorm/migrate: index %s: частичные индексы не поддерживаются на %s", ix.Name, dialect)
		}
	}
	return sx, nil
}

func columnOf(t *schema.Table, name string) (*schema.Column, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return nil, false
}

func buildColumn(c sorm.ColumnDef, dialect string) (*schema.Column, error) {
	// Atlas-парсеры типов ожидают нижний регистр ("varchar(36)", не "VARCHAR(36)").
	typ := strings.ToLower(sorm.SQLTypeFor(dialect, c))

	var col *schema.Column
	switch c.GoKind {
	case "bool":
		col = newCol(c.Nullable, func() *schema.Column { return schema.NewBoolColumn(c.Name, typ) },
			func() *schema.Column { return schema.NewNullBoolColumn(c.Name, typ) })
	case "string":
		// "varchar(36)" → тип "varchar" + размер отдельно: парсеры Atlas
		// не принимают размер внутри имени типа.
		base, size := splitSized(typ)
		var opts []schema.StringOption
		if size > 0 {
			opts = append(opts, schema.StringSize(size))
		}
		col = newCol(c.Nullable, func() *schema.Column { return schema.NewStringColumn(c.Name, base, opts...) },
			func() *schema.Column { return schema.NewNullStringColumn(c.Name, base, opts...) })
	case "float32", "float64":
		col = newCol(c.Nullable, func() *schema.Column { return schema.NewFloatColumn(c.Name, typ) },
			func() *schema.Column { return schema.NewNullFloatColumn(c.Name, typ) })
	case "time":
		col = newCol(c.Nullable, func() *schema.Column { return schema.NewTimeColumn(c.Name, typ) },
			func() *schema.Column { return schema.NewNullTimeColumn(c.Name, typ) })
	case "bytes":
		col = schema.NewNullBinaryColumn(c.Name, typ) // []byte всегда nullable
	default: // целочисленные
		col = newCol(c.Nullable, func() *schema.Column { return schema.NewIntColumn(c.Name, typ) },
			func() *schema.Column { return schema.NewNullIntColumn(c.Name, typ) })
	}

	if c.Auto {
		switch dialect {
		case "postgres":
			col.AddAttrs(&postgres.Identity{Generation: "ALWAYS"})
		case "mysql":
			col.AddAttrs(&mysql.AutoIncrement{})
		case "sqlite":
			col.AddAttrs(&sqlite.AutoIncrement{})
		}
	}
	return col, nil
}

// splitSized: "varchar(36)" → ("varchar", 36); "text" → ("text", 0).
func splitSized(typ string) (string, int) {
	open := strings.IndexByte(typ, '(')
	if open < 0 || !strings.HasSuffix(typ, ")") {
		return typ, 0
	}
	var size int
	if _, err := fmt.Sscanf(typ[open:], "(%d)", &size); err != nil {
		return typ, 0
	}
	return typ[:open], size
}

func newCol(nullable bool, notNull, null func() *schema.Column) *schema.Column {
	if nullable {
		return null()
	}
	return notNull()
}
