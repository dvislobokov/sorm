// Команда sorm — CLI генератора и миграций.
//
//	sorm gen [каталог моделей]                       кодоген sormgen
//	sorm schema -dialect <d> [-out file] [каталог]   каноническая DDL-схема
//	sorm migrate diff <имя> [флаги] [каталог]        версионная миграция через Atlas
//
// Atlas НЕ является зависимостью рантайма sorm: `migrate diff` — тонкая
// обёртка, которой нужен установленный atlas CLI (https://atlasgo.io).
// sorm генерирует schema.sql из моделей; Atlas диффует его против каталога
// миграций и пишет версионный SQL-файл.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"sorm/internal/codegen"
	"sorm/internal/ddl"
	"sorm/internal/parse"
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
		if len(os.Args) < 3 || os.Args[2] != "diff" {
			usage()
		}
		err = runMigrateDiff(os.Args[3:])
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
  sorm migrate diff <name> [-dialect postgres] [-dir migrations] [-dev-url URL] [models dir]`)
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

// devURLDefaults — dev-database, на которой Atlas нормализует и валидирует
// диф (docker:// поднимает одноразовый контейнер сам).
var devURLDefaults = map[string]string{
	"postgres": "docker://postgres/17/dev",
	"mysql":    "docker://mysql/8/dev",
	"sqlite":   "sqlite://dev?mode=memory",
}

func runMigrateDiff(args []string) error {
	fs := flag.NewFlagSet("migrate diff", flag.ExitOnError)
	dialect := fs.String("dialect", "postgres", "postgres|mysql|sqlite")
	dir := fs.String("dir", "migrations", "каталог версионных миграций")
	devURL := fs.String("dev-url", "", "dev-database для Atlas (default: docker://...)")
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

	if _, err := exec.LookPath("atlas"); err != nil {
		return fmt.Errorf("atlas CLI не найден в PATH. Установите: https://atlasgo.io/getting-started (curl -sSf https://atlasgo.sh | sh)")
	}

	schema, err := parse.Load(modelsDir)
	if err != nil {
		return err
	}
	sql, err := ddl.Generate(schema, *dialect)
	if err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "sorm-schema-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	schemaFile := filepath.Join(tmp, "schema.sql")
	if err := os.WriteFile(schemaFile, []byte(sql), 0o644); err != nil {
		return err
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	dev := *devURL
	if dev == "" {
		dev = devURLDefaults[*dialect]
	}

	cmd := exec.Command("atlas", atlasDiffArgs(name, *dir, schemaFile, dev)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	fmt.Println("sorm: exec", cmd.String())
	return cmd.Run()
}

// atlasDiffArgs — аргументы atlas migrate diff (выделено для тестируемости).
func atlasDiffArgs(name, migrationsDir, schemaFile, devURL string) []string {
	return []string{
		"migrate", "diff", name,
		"--dir", "file://" + filepath.ToSlash(migrationsDir),
		"--to", "file://" + filepath.ToSlash(schemaFile),
		"--dev-url", devURL,
	}
}
