// Package fixture loads the real PostgreSQL 17 files captured under
// testdata/pg17 (see research/fixtures) for tests. It is never linked into
// the pagerelic binary.
package fixture

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Root returns the absolute path of testdata/pg17.
func Root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", "pg17")
}

// Bytes returns the decompressed content of testdata/pg17/<rel>.gz.
func Bytes(t testing.TB, rel string) []byte {
	t.Helper()
	f, err := os.Open(filepath.Join(Root(), filepath.FromSlash(rel)+".gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// JSON decodes testdata/pg17/<rel>.gz into v.
func JSON(t testing.TB, rel string, v any) {
	t.Helper()
	if err := json.Unmarshal(Bytes(t, rel), v); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
}

// JSONL decodes a JSON-lines fixture into a slice of string maps.
func JSONL(t testing.TB, rel string) []map[string]*string {
	t.Helper()
	var out []map[string]*string
	sc := bufio.NewScanner(strings.NewReader(string(Bytes(t, rel))))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var m map[string]*string
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// Extract writes the named fixtures (paths relative to testdata/pg17,
// without .gz) under dir, preserving relative paths, and returns dir.
func Extract(t testing.TB, dir string, rels ...string) string {
	t.Helper()
	for _, rel := range rels {
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, Bytes(t, rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// DataDir materialises the raw PostgreSQL data directory snapshot and
// returns its root (the equivalent of $PGDATA).
func DataDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	var rels []string
	root := filepath.Join(Root(), "raw")
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			rel, _ := filepath.Rel(root, p)
			rels = append(rels, "raw/"+filepath.ToSlash(strings.TrimSuffix(rel, ".gz")))
		}
		return nil
	})
	Extract(t, dir, rels...)
	return filepath.Join(dir, "raw")
}
