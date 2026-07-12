package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Чексуммы каталога миграций: файл sorm.sum фиксирует содержимое каждого
// *.sql (включая down-файлы). Up/Down/Pending сверяют каталог с суммами —
// переписанная задним числом или подложенная миграция обнаруживается до
// какого-либо SQL. Diff обновляет sorm.sum после записи новых файлов.
//
// Каталоги без sorm.sum принимаются (обратная совместимость и рукописные
// миграции): включение — первым же запуском Diff или WriteSum.

// SumFile — имя файла контрольных сумм в каталоге миграций.
const SumFile = "sorm.sum"

// SumError — расхождение каталога миграций с sorm.sum.
type SumError struct {
	Modified []string // файл есть в sorm.sum, но содержимое изменилось
	Missing  []string // файл есть в sorm.sum, но отсутствует на диске
	Extra    []string // файл на диске, но не в sorm.sum
}

func (e *SumError) Error() string {
	return fmt.Sprintf("github.com/dvislobokov/sorm/migrate: каталог миграций не совпадает с %s: изменены %v, отсутствуют %v, лишние %v",
		SumFile, e.Modified, e.Missing, e.Extra)
}

// WriteSum пересчитывает sorm.sum по текущему содержимому каталога.
func WriteSum(dir string) error {
	sums, err := dirChecksums(dir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(sums))
	for name := range sums {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		fmt.Fprintf(&b, "%s  %s\n", sums[name], name)
	}
	return os.WriteFile(filepath.Join(dir, SumFile), []byte(b.String()), 0o644)
}

// VerifySum сверяет каталог с sorm.sum; отсутствие sorm.sum — не ошибка.
func VerifySum(dir string) error {
	content, err := os.ReadFile(filepath.Join(dir, SumFile))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	want := map[string]string{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, "  ")
		if !ok {
			return fmt.Errorf("github.com/dvislobokov/sorm/migrate: битая строка в %s: %q", SumFile, line)
		}
		want[name] = sum
	}

	got, err := dirChecksums(dir)
	if err != nil {
		return err
	}

	var se SumError
	for name, sum := range want {
		g, ok := got[name]
		switch {
		case !ok:
			se.Missing = append(se.Missing, name)
		case g != sum:
			se.Modified = append(se.Modified, name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			se.Extra = append(se.Extra, name)
		}
	}
	if len(se.Modified)+len(se.Missing)+len(se.Extra) > 0 {
		sort.Strings(se.Modified)
		sort.Strings(se.Missing)
		sort.Strings(se.Extra)
		return &se
	}
	return nil
}

func dirChecksums(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		h := sha256.Sum256(content)
		out[e.Name()] = "h1:" + hex.EncodeToString(h[:])
	}
	return out, nil
}
