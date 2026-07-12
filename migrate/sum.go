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

// Migration directory checksums: the sorm.sum file pins the contents of
// every *.sql (including down files). Up/Down/Pending verify the directory
// against the sums — a retroactively rewritten or planted migration is
// detected before any SQL runs. Diff updates sorm.sum after writing new
// files.
//
// Directories without sorm.sum are accepted (backward compatibility and
// hand-written migrations): opt in with the first run of Diff or WriteSum.

// SumFile is the name of the checksum file in the migration directory.
const SumFile = "sorm.sum"

// SumError reports a mismatch between the migration directory and sorm.sum.
type SumError struct {
	Modified []string // file is in sorm.sum, but its contents changed
	Missing  []string // file is in sorm.sum, but absent on disk
	Extra    []string // file is on disk, but not in sorm.sum
}

func (e *SumError) Error() string {
	return fmt.Sprintf("github.com/dvislobokov/sorm/migrate: migration directory does not match %s: modified %v, missing %v, extra %v",
		SumFile, e.Modified, e.Missing, e.Extra)
}

// WriteSum recomputes sorm.sum from the current directory contents.
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

// VerifySum checks the directory against sorm.sum; a missing sorm.sum is not an error.
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
			return fmt.Errorf("github.com/dvislobokov/sorm/migrate: malformed line in %s: %q", SumFile, line)
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
