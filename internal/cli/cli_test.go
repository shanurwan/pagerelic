package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shanurwan/pagerelic/internal/fixture"
	"github.com/shanurwan/pagerelic/internal/page"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(context.Background(), append([]string{"pagerelic"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func must(t *testing.T, want int, args ...string) string {
	t.Helper()
	code, out, errs := run(t, args...)
	if code != want {
		t.Fatalf("pagerelic %s: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), code, want, trunc(out), trunc(errs))
	}
	return out
}

func trunc(s string) string {
	if len(s) > 3000 {
		return s[:3000] + "…"
	}
	return s
}

func jsonl(t *testing.T, s string) []map[string]any {
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad JSON line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func TestCatalogDrivenRecovery(t *testing.T) {
	dd := fixture.DataDir(t)
	out := must(t, ExitOK, "relations", "--datadir", dd)
	if !strings.Contains(out, "lab") {
		t.Fatalf("databases:\n%s", out)
	}
	out = must(t, ExitOK, "relations", "--datadir", dd, "--db", "lab")
	if !strings.Contains(out, "public.people") || !strings.Contains(out, "tags text[]") {
		t.Fatalf("relations:\n%s", out)
	}
	rows := jsonl(t, must(t, ExitOK, "rows", "--datadir", dd, "--db", "lab", "--table", "people"))
	if len(rows) != 360 {
		t.Fatalf("people rows = %d", len(rows))
	}
	var r7 map[string]any
	for _, r := range rows {
		if r["id"] == "7" {
			r7 = r
		}
	}
	if note, _ := r7["note"].(string); !strings.HasPrefix(note, "compressible toast value") || len(note) != 10000 {
		t.Fatalf("TOASTed note not rebuilt via catalog-resolved TOAST relation: %d bytes", len(note))
	}
	csvOut := must(t, ExitOK, "rows", "--datadir", dd, "--db", "16388", "--table", "blobs", "--format", "csv")
	if lines := strings.Count(csvOut, "\n"); lines != 5 {
		t.Fatalf("csv lines = %d", lines)
	}
	must(t, ExitOK, "xact", "--datadir", dd, "3")
	must(t, ExitOK, "verify", dd)
}

func TestSchemaRecoveryWithManifestAndRemnants(t *testing.T) {
	dir := fixture.Extract(t, t.TempDir(), "stages/C-vacuumed/accounts.heap", "stages/C-vacuumed/pg_xact_0000")
	stage := filepath.Join(dir, "stages", "C-vacuumed")
	xactDir := filepath.Join(stage, "xact")
	os.MkdirAll(xactDir, 0o755)
	os.Rename(filepath.Join(stage, "pg_xact_0000"), filepath.Join(xactDir, "0000"))
	out := filepath.Join(t.TempDir(), "deleted.jsonl")
	spec := "id int4, owner text, balance numeric(12,2), opened timestamptz, memo text"
	must(t, ExitOK, "rows", "--schema", spec, "--xact", xactDir, "--remnants", "--state", "deleted", "--out", out, filepath.Join(stage, "accounts.heap"))
	b, _ := os.ReadFile(out)
	rows := jsonl(t, string(b))
	if len(rows) < 20 {
		t.Fatalf("recovered only %d deleted rows from a vacuumed table", len(rows))
	}
	for _, r := range rows {
		if r["_source"] != "remnant" || r["_state"] != "deleted" {
			t.Fatalf("unexpected row %v", r)
		}
	}
	var m manifest
	mb, err := os.ReadFile(out + ".manifest.json")
	if err != nil || json.Unmarshal(mb, &m) != nil || m.Output.SHA256 == "" || len(m.Inputs) != 1 {
		t.Fatalf("manifest: %s %v", mb, err)
	}
	// Output is never overwritten.
	must(t, ExitFailure, "rows", "--schema", spec, "--out", out, filepath.Join(stage, "accounts.heap"))
}

func TestCorruptionExitCodes(t *testing.T) {
	good := fixture.Bytes(t, "stages/A-intact/accounts.heap")
	bad := append([]byte(nil), good...)
	bad[2*page.DefaultSize+5000] ^= 0xFF
	p := filepath.Join(t.TempDir(), "16464")
	os.WriteFile(p, bad, 0o644)
	out := must(t, ExitPartial, "verify", p)
	if !strings.Contains(out, "block 2") || !strings.Contains(out, "checksum mismatch") {
		t.Fatalf("verify output:\n%s", out)
	}
	must(t, ExitPartial, "rows", "--schema", "id int4, owner text, balance numeric, opened timestamptz, memo text", p)
	insp := must(t, ExitPartial, "inspect", "--block", "2", p)
	if !strings.Contains(insp, "MISMATCH") || !strings.Contains(insp, "line pointers") {
		t.Fatalf("inspect output:\n%s", insp)
	}
}

func TestCarveThenDecode(t *testing.T) {
	accounts := fixture.Bytes(t, "stages/A-intact/accounts.heap")
	rng := rand.New(rand.NewSource(9))
	var disk []byte
	for blk := 0; blk*page.DefaultSize < len(accounts); blk++ {
		gap := make([]byte, 4096*(1+rng.Intn(2)))
		rng.Read(gap)
		disk = append(disk, gap...)
		disk = append(disk, accounts[blk*page.DefaultSize:(blk+1)*page.DefaultSize]...)
	}
	img := filepath.Join(t.TempDir(), "disk.img")
	os.WriteFile(img, disk, 0o644)
	xactDir := filepath.Join(t.TempDir(), "pg_xact")
	os.MkdirAll(xactDir, 0o755)
	os.WriteFile(filepath.Join(xactDir, "0000"), fixture.Bytes(t, "stages/A-intact/pg_xact_0000"), 0o644)
	outDir := filepath.Join(t.TempDir(), "carved")
	out := must(t, ExitOK, "carve", "--out", outDir, img)
	if !strings.Contains(out, "carved 14 pages") {
		t.Fatalf("carve:\n%s", out)
	}
	// Without the commit log, rows lacking hint bits are "unknown", never guessed.
	unknown := jsonl(t, must(t, ExitOK, "rows", "--checksums", "off", "--state", "unknown",
		"--schema", "id int4, owner text, balance numeric(12,2), opened timestamptz, memo text", filepath.Join(outDir, "heap-natts5.pages")))
	if len(unknown) == 0 {
		t.Fatal("expected unhinted rows to be unknown without pg_xact")
	}
	rows := jsonl(t, must(t, ExitOK, "rows", "--checksums", "off", "--state", "live", "--xact", xactDir,
		"--schema", "id int4, owner text, balance numeric(12,2), opened timestamptz, memo text", filepath.Join(outDir, "heap-natts5.pages")))
	if len(rows) != 800 {
		t.Fatalf("live rows from carved pages = %d, want 800", len(rows))
	}
	must(t, ExitFailure, "carve", "--out", outDir, img) // non-empty output dir
}

func TestUsage(t *testing.T) {
	must(t, ExitUsage)
	must(t, ExitUsage, "nope")
	must(t, ExitUsage, "rows")
	must(t, ExitUsage, "rows", "--schema", "id wibble", "x")
	must(t, ExitOK, "version")
}
