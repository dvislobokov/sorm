// Команда sorm — CLI генератора и миграций.
//
//	sorm gen [каталог моделей]                       кодоген sormgen
//	sorm schema -dialect <d> [-out file] [каталог]   каноническая DDL-схема
//	sorm migrate diff <имя> [флаги] [каталог]        сгенерировать версионную миграцию
//	sorm migrate up -dsn <dsn> [флаги]               применить версионные миграции
//
// Внешних инструментов не требуется: движок диффа (ariga.io/atlas SDK)
// встроен. Для diff на PostgreSQL/MySQL нужна пустая scratch-БД —
// пользователь поднимает её сам и передаёт -dev-dsn; для SQLite scratch
// по умолчанию in-memory и ничего поднимать не нужно.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/internal/codegen"
	"github.com/dvislobokov/sorm/internal/ddl"
	"github.com/dvislobokov/sorm/internal/parse"
	"github.com/dvislobokov/sorm/migrate"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = runGen(argDir(os.Args[2:]))
	case "schema":
		err = runSchema(os.Args[2:])
	case "migrate":
		if len(os.Args) < 3 {
			usage()
		}
		switch os.Args[2] {
		case "diff":
			err = runMigrateDiff(os.Args[3:])
		case "up":
			err = runMigrateUp(os.Args[3:])
		default:
			usage()
		}
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sorm:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  sorm gen [models dir]
  sorm schema -dialect postgres|mysql|sqlite [-out schema.sql] [models dir]
  sorm migrate diff [-dialect postgres] [-dir migrations] [-dev-dsn DSN] <name> [models dir]
  sorm migrate up -dsn DSN [-dialect postgres] [-dir migrations]
(флаги — до позиционных аргументов)`)
	os.Exit(2)
}

func argDir(args []string) string {
	for _, a := range args {
		if len(a) > 0 && a[0] != '-' {
			return a
		}
	}
	return "."
}

func runGen(dir string) error {
	schema, err := parse.Load(dir)
	if err != nil {
		return err
	}
	files, err := codegen.Generate(schema)
	if err != nil {
		return err
	}
	outDir := filepath.Join(dir, "sormgen")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(outDir, name), content, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("sorm: generated %d file(s) for %d entit(ies) in %s\n",
		len(files), len(schema.Entities), outDir)
	return nil
}

func runSchema(args []string) error {
	fs := flag.NewFlagSet("schema", flag.ExitOnError)
	dialect := fs.String("dialect", "postgres", "postgres|mysql|sqlite")
	out := fs.String("out", "", "файл вывода (по умолчанию stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}

	schema, err := parse.Load(dir)
	if err != nil {
		return err
	}
	sql, err := ddl.Generate(schema, *dialect)
	if err != nil {
		return err
	}
	if *out == "" {
		fmt.Print(sql)
		return nil
	}
	if err := os.WriteFile(*out, []byte(sql), 0o644); err != nil {
		return err
	}
	fmt.Printf("sorm: schema written to %s (%s)\n", *out, *dialect)
	return nil
}

// driverNames: диалект → имя драйвера database/sql.
var driverNames = map[string]string{
	"postgres": "pgx",
	"mysql":    "mysql",
	"sqlite":   "sqlite",
}

// registerModels загружает пакет моделей и регистрирует TableDef'ы —
// после этого sorm/migrate видит желаемую схему без импорта sormgen.
func registerModels(modelsDir string) error {
	schema, err := parse.Load(modelsDir)
	if err != nil {
		return err
	}
	for _, e := range schema.Entities {
		def, err := ddl.TableDefOf(schema, e)
		if err != nil {
			return err
		}
		if e.HasIndexesMethod {
			fmt.Fprintf(os.Stderr,
				"sorm: ВНИМАНИЕ: %s определяет кастомные Indexes() — CLI не исполняет код моделей и не увидит их.\n"+
					"       Для полной схемы используйте sorm/migrate из кода (Diff/Apply с импортом sormgen).\n", e.Name)
		}
		sorm.RegisterTable(def)
	}
	joins, err := ddl.JoinTableDefs(schema)
	if err != nil {
		return err
	}
	for _, def := range joins {
		sorm.RegisterTable(def)
	}
	return nil
}

func openDB(dialect, dsn string) (*sql.DB, error) {
	drv, ok := driverNames[dialect]
	if !ok {
		return nil, fmt.Errorf("unknown dialect %q (postgres|mysql|sqlite)", dialect)
	}
	return sql.Open(drv, dsn)
}

func runMigrateDiff(args []string) error {
	fs := flag.NewFlagSet("migrate diff", flag.ExitOnError)
	dialect := fs.String("dialect", "postgres", "postgres|mysql|sqlite")
	dir := fs.String("dir", "migrations", "каталог версионных миграций")
	devDSN := fs.String("dev-dsn", "", "DSN пустой scratch-БД для replay (sqlite: по умолчанию in-memory)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("migrate diff: не задано имя миграции")
	}
	name := fs.Arg(0)
	modelsDir := "."
	if fs.NArg() > 1 {
		modelsDir = fs.Arg(1)
	}

	dsn := *devDSN
	if dsn == "" {
		if *dialect != "sqlite" {
			return fmt.Errorf("migrate diff: для %s нужна пустая scratch-БД — поднимите её сами и передайте -dev-dsn", *dialect)
		}
		dsn = ":memory:"
	}

	if err := registerModels(modelsDir); err != nil {
		return err
	}
	dev, err := openDB(*dialect, dsn)
	if err != nil {
		return err
	}
	defer dev.Close()
	if *dialect == "sqlite" {
		dev.SetMaxOpenConns(1)
	}

	fname, err := migrate.Diff(context.Background(), dev, *dialect, *dir, name)
	if err != nil {
		return err
	}
	if fname == "" {
		fmt.Println("sorm: изменений нет — миграция не создана")
		return nil
	}
	fmt.Printf("sorm: создана миграция %s\n", filepath.Join(*dir, fname))
	return nil
}

func runMigrateUp(args []string) error {
	fs := flag.NewFlagSet("migrate up", flag.ExitOnError)
	dialect := fs.String("dialect", "postgres", "postgres|mysql|sqlite")
	dir := fs.String("dir", "migrations", "каталог версионных миграций")
	dsn := fs.String("dsn", "", "DSN целевой БД (обязательно)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return fmt.Errorf("migrate up: требуется -dsn целевой БД")
	}

	db, err := openDB(*dialect, *dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	applied, err := migrate.Up(context.Background(), db, *dialect, *dir)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Println("sorm: всё уже применено")
		return nil
	}
	for _, f := range applied {
		fmt.Println("sorm: применена", f)
	}
	return nil
}
