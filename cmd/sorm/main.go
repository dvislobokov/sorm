// Command sorm is the code generator and migration CLI.
//
//	sorm gen [models dir]                            generate the sormgen package
//	sorm schema -dialect <d> [-out file] [dir]       canonical DDL schema
//	sorm migrate diff <name> [flags] [dir]           generate a versioned migration
//	sorm migrate up -dsn <dsn> [flags]               apply versioned migrations
//
// No external tools are required: the diff engine (ariga.io/atlas SDK)
// is built in. Diff on PostgreSQL/MySQL needs an empty scratch database —
// the user provisions it and passes -dev-dsn; for SQLite the scratch DB
// is in-memory by default, so nothing needs to be set up.
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
		err = runGen(os.Args[2:])
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
  sorm gen [-naming snake|camel|pascal] [models dir]
  sorm schema -dialect postgres|mysql|sqlite [-out schema.sql] [-naming ...] [models dir]
  sorm migrate diff [-dialect postgres] [-dir migrations] [-dev-dsn DSN] [-naming ...] <name> [models dir]
  sorm migrate up -dsn DSN [-dialect postgres] [-dir migrations]
(flags go before positional arguments; -naming must match across commands)`)
	os.Exit(2)
}

func runGen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	naming := fs.String("naming", "snake", "identifier naming strategy: snake|camel|pascal")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	schema, err := parse.Load(dir, parse.WithNaming(*naming))
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
	out := fs.String("out", "", "output file (default stdout)")
	naming := fs.String("naming", "snake", "identifier naming strategy: snake|camel|pascal")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}

	schema, err := parse.Load(dir, parse.WithNaming(*naming))
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

// driverNames: dialect → database/sql driver name.
var driverNames = map[string]string{
	"postgres": "pgx",
	"mysql":    "mysql",
	"sqlite":   "sqlite",
}

// registerModels loads the models package and registers TableDefs —
// after that sorm/migrate sees the desired schema without importing sormgen.
func registerModels(modelsDir, naming string) error {
	schema, err := parse.Load(modelsDir, parse.WithNaming(naming))
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
				"sorm: WARNING: %s defines custom Indexes() — the CLI does not execute model code and will not see them.\n"+
					"       For the full schema use sorm/migrate from code (Diff/Apply importing sormgen).\n", e.Name)
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
	dir := fs.String("dir", "migrations", "versioned migrations directory")
	devDSN := fs.String("dev-dsn", "", "DSN of an empty scratch DB for replay (sqlite: in-memory by default)")
	naming := fs.String("naming", "snake", "identifier naming strategy: snake|camel|pascal (must match sorm gen)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("migrate diff: migration name is required")
	}
	name := fs.Arg(0)
	modelsDir := "."
	if fs.NArg() > 1 {
		modelsDir = fs.Arg(1)
	}

	dsn := *devDSN
	if dsn == "" {
		if *dialect != "sqlite" {
			return fmt.Errorf("migrate diff: %s needs an empty scratch DB — provision it yourself and pass -dev-dsn", *dialect)
		}
		dsn = ":memory:"
	}

	if err := registerModels(modelsDir, *naming); err != nil {
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
		fmt.Println("sorm: no changes — migration not created")
		return nil
	}
	fmt.Printf("sorm: created migration %s\n", filepath.Join(*dir, fname))
	return nil
}

func runMigrateUp(args []string) error {
	fs := flag.NewFlagSet("migrate up", flag.ExitOnError)
	dialect := fs.String("dialect", "postgres", "postgres|mysql|sqlite")
	dir := fs.String("dir", "migrations", "versioned migrations directory")
	dsn := fs.String("dsn", "", "target database DSN (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return fmt.Errorf("migrate up: target database -dsn is required")
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
		fmt.Println("sorm: everything already applied")
		return nil
	}
	for _, f := range applied {
		fmt.Println("sorm: applied", f)
	}
	return nil
}
