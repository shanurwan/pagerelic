package catalog_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shanurwan/pagerelic/internal/catalog"
	"github.com/shanurwan/pagerelic/internal/fixture"
	"github.com/shanurwan/pagerelic/internal/heap"
	"github.com/shanurwan/pagerelic/internal/page"
	"github.com/shanurwan/pagerelic/internal/recover"
	"github.com/shanurwan/pagerelic/internal/relation"
	"github.com/shanurwan/pagerelic/internal/xact"
)

type keyCatalog struct {
	PeopleColumns []struct {
		Attnum   int         `json:"attnum"`
		Attname  string      `json:"attname"`
		Atttypid json.Number `json:"atttypid"`
		Attlen   int16       `json:"attlen"`
		Attalign string      `json:"attalign"`
	} `json:"people_columns"`
	Relations []struct {
		OID           json.Number `json:"oid"`
		Relname       string      `json:"relname"`
		Relfilenode   json.Number `json:"relfilenode"`
		Reltoastrelid json.Number `json:"reltoastrelid"`
	} `json:"relations"`
}

func openRaw(t *testing.T) *catalog.DB {
	dir := fixture.DataDir(t)
	xl, err := xact.Open(filepath.Join(dir, "pg_xact"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := catalog.Open(filepath.Join(dir, "base", "16388"), xl)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestBootstrapMatchesPgAttribute(t *testing.T) {
	var key keyCatalog
	fixture.JSON(t, "key/catalog.json", &key)
	db := openRaw(t)

	people, err := db.Find("public.people")
	if err != nil {
		t.Fatal(err)
	}
	if len(people.Columns) != len(key.PeopleColumns) {
		t.Fatalf("columns = %d, want %d", len(people.Columns), len(key.PeopleColumns))
	}
	for i, k := range key.PeopleColumns {
		c := people.Columns[i]
		if c.Name != k.Attname || k.Atttypid.String() != itoa(c.TypeOID) || c.Len != k.Attlen || string(c.Align) != k.Attalign {
			t.Errorf("column %d: got %+v want %+v", i+1, c, k)
		}
	}
	for _, k := range key.Relations {
		if k.Relname != "people" && k.Relname != "blobs" {
			continue
		}
		r, err := db.Find(k.Relname)
		if err != nil {
			t.Fatal(err)
		}
		if itoa(r.OID) != k.OID.String() || itoa(r.FileNode) != k.Relfilenode.String() || itoa(r.ToastOID) != k.Reltoastrelid.String() {
			t.Errorf("%s: got oid=%d filenode=%d toast=%d", k.Relname, r.OID, r.FileNode, r.ToastOID)
		}
		if r.ToastPath == "" {
			t.Errorf("%s: TOAST path not resolved", k.Relname)
		}
	}
}

func itoa(v uint32) string { return json.Number(jsonItoa(v)).String() }

func jsonItoa(v uint32) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestDroppedTableRecovery is the headline scenario: the table was dropped,
// its file unlinked. Its schema comes back from deleted catalog tuples and
// its rows from a copy of its pages (as carved off disk).
func TestDroppedTableRecovery(t *testing.T) {
	dir := t.TempDir()
	fixture.Extract(t, dir, "dropped/1259", "dropped/1249", "dropped/1247", "dropped/pg_filenode.map", "dropped/invoices.heap", "dropped/pg_xact_0000")
	dbDir := filepath.Join(dir, "dropped")
	os.Rename(filepath.Join(dbDir, "pg_xact_0000"), filepath.Join(dbDir, "xact0000"))
	xl := xact.FromSegment(0, mustRead(t, filepath.Join(dbDir, "xact0000")))
	db, err := catalog.Open(dbDir, xl)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Find("invoices"); err != nil {
		t.Fatalf("dropped table not found among deleted catalog rows: %v", err)
	}
	var inv catalog.Relation
	for _, r := range db.Relations(true) {
		if r.Name == "invoices" {
			inv = r
		}
	}
	if inv.State != heap.Deleted {
		t.Fatalf("invoices state %s, want deleted", inv.State)
	}
	want := "no int8, customer text, amount numeric, issued date, paid bool"
	got := ""
	for i, c := range inv.Columns {
		if i > 0 {
			got += ", "
		}
		got += c.Name + " " + c.TypeName
	}
	if got != want {
		t.Fatalf("recovered schema %q, want %q", got, want)
	}
	rel := relation.FromBytes("invoices", mustRead(t, filepath.Join(dbDir, "invoices.heap")), page.DefaultSize)
	var rows []recover.Row
	if _, err := recover.Scan(context.Background(), rel, recover.Options{Columns: inv.Columns, Xact: xl}, nil,
		func(r recover.Row) error { rows = append(rows, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 120 {
		t.Fatalf("rows = %d, want 120", len(rows))
	}
	r := rows[41] // no = 42
	if r.Values[0].Text != "42" || r.Values[1].Text != "customer 8" || r.Values[2].Text != "839.58" || r.Values[3].Text != "2023-02-12" || r.Values[4].Text != "t" {
		t.Fatalf("row 42 = %+v", r.Values)
	}
}

func mustRead(t *testing.T, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFileNodeMapRejectsCorruption(t *testing.T) {
	b := fixture.Bytes(t, "raw/base/16388/pg_filenode.map")
	m, err := catalog.ParseFileNodeMap(b)
	if err != nil || m[catalog.OIDPgClass] == 0 {
		t.Fatalf("map %v, %v", m, err)
	}
	b[20] ^= 1
	if _, err := catalog.ParseFileNodeMap(b); err == nil {
		t.Fatal("CRC corruption not detected")
	}
}
