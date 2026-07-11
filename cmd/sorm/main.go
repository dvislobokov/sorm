// Команда sorm — CLI генератора.
//
//	sorm gen [каталог пакета моделей]      (по умолчанию ".")
//
// Результат — пакет sormgen в подкаталоге: дескрипторы колонок и мета.
// Генерация детерминирована; ошибки валидации схемы — до записи файлов.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"sorm/cmd/sorm/internal/codegen"
	"sorm/cmd/sorm/internal/parse"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "gen" {
		fmt.Fprintln(os.Stderr, "usage: sorm gen [models package dir]")
		os.Exit(2)
	}
	dir := "."
	if len(os.Args) > 2 {
		dir = os.Args[2]
	}
	if err := run(dir); err != nil {
		fmt.Fprintln(os.Stderr, "sorm:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
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
