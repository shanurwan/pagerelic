package recover_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/shanurwan/pagerelic/internal/fixture"
	"github.com/shanurwan/pagerelic/internal/heap"
	"github.com/shanurwan/pagerelic/internal/page"
	"github.com/shanurwan/pagerelic/internal/recover"
	"github.com/shanurwan/pagerelic/internal/relation"
	"github.com/shanurwan/pagerelic/internal/schema"
	"github.com/shanurwan/pagerelic/internal/toast"
	"github.com/shanurwan/pagerelic/internal/xact"
)

type keyColumn struct {
	Attnum   int         `json:"attnum"`
	Attname  string      `json:"attname"`
	Atttypid json.Number `json:"atttypid"` // PostgreSQL's JSON renders oid as a string
	Attlen   int16       `json:"attlen"`
	Attalign string      `json:"attalign"`
	Attbyval bool        `json:"attbyval"`
}

// peopleColumns builds the layout from PostgreSQL's own pg_attribute rows.
func peopleColumns(t *testing.T) []schema.Column {
	var cat struct {
		PeopleColumns []keyColumn `json:"people_columns"`
	}
	fixture.JSON(t, "key/catalog.json", &cat)
	var cols []schema.Column
	for _, c := range cat.PeopleColumns {
		oid, err := c.Atttypid.Int64()
		if err != nil {
			t.Fatal(err)
		}
		cols = append(cols, schema.Column{Name: c.Attname, TypeOID: uint32(oid), Len: c.Attlen, Align: c.Attalign[0], ByVal: c.Attbyval})
	}
	return cols
}

func rowMap(cols []schema.Column, r recover.Row) map[string]*string {
	m := map[string]*string{}
	for i, c := range schema.Visible(cols) {
		v := r.Values[i]
		if v.Null {
			m[c.Name] = nil
		} else {
			s := v.Text
			m[c.Name] = &s
		}
	}
	return m
}

func diff(got, want map[string]*string) string {
	var d []string
	for k, w := range want {
		g := got[k]
		switch {
		case g == nil && w == nil:
		case g == nil || w == nil || *g != *w:
			gs, ws := "NULL", "NULL"
			if g != nil {
				gs = *g
			}
			if w != nil {
				ws = *w
			}
			if len(gs) > 80 {
				gs = gs[:80] + "…"
			}
			if len(ws) > 80 {
				ws = ws[:80] + "…"
			}
			d = append(d, fmt.Sprintf("%s: got %q want %q", k, gs, ws))
		}
	}
	sort.Strings(d)
	return strings.Join(d, "; ")
}

func byID(rows []map[string]*string) map[string]map[string]*string {
	m := map[string]map[string]*string{}
	for _, r := range rows {
		// The answer key was exported with ::text casts, which differ from
		// the type output functions PageRelic reproduces (and psql shows):
		// bool::text is true/false (boolout: t/f) and bpchar::text trims
		// padding (bpcharout keeps it). Normalise the key to output-function form.
		if v := r["active"]; v != nil {
			s := map[string]string{"true": "t", "false": "f"}[*v]
			r["active"] = &s
		}
		if v := r["code"]; v != nil {
			s := fmt.Sprintf("%-4s", *v)
			r["code"] = &s
		}
		m[*r["id"]] = r
	}
	return m
}

func scan(t *testing.T, heapBytes []byte, opt recover.Options) ([]recover.Row, recover.Summary) {
	t.Helper()
	var rows []recover.Row
	sum, err := recover.Scan(context.Background(), relation.FromBytes("rel", heapBytes, page.DefaultSize), opt, nil,
		func(r recover.Row) error { rows = append(rows, r); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return rows, sum
}

func loadToast(t *testing.T, rel string) *toast.Store {
	ts, err := toast.Load(relation.FromBytes(rel, fixture.Bytes(t, rel), page.DefaultSize))
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// TestPeopleMatchesPostgreSQLOutput decodes every tuple of the real table
// and compares every column with PostgreSQL's own text output.
func TestPeopleMatchesPostgreSQLOutput(t *testing.T) {
	cols := peopleColumns(t)
	orig := byID(fixture.JSONL(t, "key/expected_orig.jsonl"))
	live := byID(fixture.JSONL(t, "key/expected_live.jsonl"))
	opt := recover.Options{
		Columns: cols,
		Xact:    xact.FromSegment(0, fixture.Bytes(t, "raw/pg_xact/0000")),
		Toast:   loadToast(t, "raw/base/16388/16437"),
	}
	rows, sum := scan(t, fixture.Bytes(t, "raw/base/16388/16434"), opt)
	if sum.ChecksumFailures != 0 || sum.InvalidPages != 0 {
		t.Fatalf("clean fixture reported problems: %+v", sum)
	}
	seenLive := map[string]bool{}
	for _, r := range rows {
		m := rowMap(cols, r)
		id := *m["id"]
		if r.State == heap.Live {
			if d := diff(m, live[id]); d != "" {
				t.Fatalf("live row %s (%d,%d): %s", id, r.Block, r.Item, d)
			}
			seenLive[id] = true
		} else if d := diff(m, orig[id]); d != "" {
			t.Fatalf("%s row %s: %s", r.State, id, d)
		}
		if r.Confidence != recover.High {
			t.Fatalf("row %s confidence %s: %v", id, r.Confidence, r.Notes)
		}
	}
	if len(seenLive) != len(live) {
		t.Fatalf("recovered %d live rows, PostgreSQL has %d", len(seenLive), len(live))
	}
	t.Logf("people: %d tuple versions via line pointers, states %v", len(rows), sum.ByState)
}

func TestToastVariants(t *testing.T) {
	cols, err := schema.Parse("id int4, kind text, body text")
	if err != nil {
		t.Fatal(err)
	}
	want := byID(fixture.JSONL(t, "key/expected_blobs.jsonl"))
	rows, _ := scan(t, fixture.Bytes(t, "raw/base/16388/16442"), recover.Options{Columns: cols, Toast: loadToast(t, "raw/base/16388/16445")})
	if len(rows) != 4 {
		t.Fatalf("rows = %d", len(rows))
	}
	for _, r := range rows {
		m := rowMap(cols, r)
		if d := diff(m, want[*m["id"]]); d != "" {
			t.Fatalf("%s: %s", *m["kind"], d)
		}
	}
	// Without the TOAST relation, external values are reported, not guessed.
	rows, _ = scan(t, fixture.Bytes(t, "raw/base/16388/16442"), recover.Options{Columns: cols})
	for _, r := range rows {
		if strings.HasPrefix(*rowMap(cols, r)["kind"], "external") && !strings.Contains(r.Values[2].Err, "TOAST") {
			t.Fatalf("external value without store: %+v", r.Values[2])
		}
	}
}

// TestPruningExperiment runs line-pointer decoding plus remnant carving on
// the three stages of the controlled experiment. Every recovered row must
// be byte-identical to a row version PostgreSQL really wrote (zero false
// positives); the counts are the research result.
func TestPruningExperiment(t *testing.T) {
	cols, err := schema.Parse("id int4, owner text, balance numeric(12,2), opened timestamptz, memo text")
	if err != nil {
		t.Fatal(err)
	}
	orig := byID(fixture.JSONL(t, "stages/expected_orig.jsonl"))
	live := byID(fixture.JSONL(t, "stages/expected_live.jsonl"))
	for _, stage := range []string{"A-intact", "B-pruned", "C-vacuumed"} {
		opt := recover.Options{Columns: cols, Xact: xact.FromSegment(0, fixture.Bytes(t, "stages/"+stage+"/pg_xact_0000")), Remnants: true}
		rows, sum := scan(t, fixture.Bytes(t, "stages/"+stage+"/accounts.heap"), opt)
		deletedIDs := map[string]bool{}
		oldVersions := map[string]bool{}
		bySource := map[string]int{}
		for _, r := range rows {
			m := rowMap(cols, r)
			id := *m["id"]
			bySource[r.Source]++
			if strings.HasPrefix(*m["owner"], "ghost-") {
				if r.State != heap.Aborted {
					t.Fatalf("%s: ghost row state %s", stage, r.State)
				}
				continue
			}
			okOrig := diff(m, orig[id]) == ""
			okLive := live[id] != nil && diff(m, live[id]) == ""
			if !okOrig && !okLive {
				t.Fatalf("%s: FALSE POSITIVE %s row %s at (%d, off %d): %s", stage, r.Source, id, r.Block, r.Offset, diff(m, orig[id]))
			}
			if live[id] == nil {
				deletedIDs[id] = true
			} else if okOrig && !okLive {
				oldVersions[id] = true
			}
		}
		if sum.ByState[heap.Live] != 800 {
			t.Errorf("%s: live rows %d, want exactly 800 (stale remnant copies must be de-duplicated)", stage, sum.ByState[heap.Live])
		}
		t.Logf("%s: sources %v; states %v; deleted rows recovered %d/200; pre-update versions recovered %d/89",
			stage, bySource, sum.ByState, len(deletedIDs), len(oldVersions))
	}
}

// TestCorruptionIsContained injects damage into real pages.
func TestCorruptionIsContained(t *testing.T) {
	cols, _ := schema.Parse("id int4, owner text, balance numeric(12,2), opened timestamptz, memo text")
	good := fixture.Bytes(t, "stages/A-intact/accounts.heap")
	baseRows, _ := scan(t, good, recover.Options{Columns: cols})

	t.Run("bit flip -> checksum mismatch, rows kept at low confidence", func(t *testing.T) {
		b := append([]byte(nil), good...)
		b[3*page.DefaultSize+6000] ^= 0x20
		rows, sum := scan(t, b, recover.Options{Columns: cols})
		if sum.ChecksumFailures != 1 || len(rows) != len(baseRows) {
			t.Fatalf("failures=%d rows=%d/%d", sum.ChecksumFailures, len(rows), len(baseRows))
		}
		low := 0
		for _, r := range rows {
			if r.Block == 3 && r.Confidence == recover.Low {
				low++
			}
		}
		if low == 0 {
			t.Fatal("rows on the corrupted page were not downgraded")
		}
	})
	t.Run("zeroed header -> remnant carving still recovers the tuples", func(t *testing.T) {
		b := append([]byte(nil), good...)
		clear(b[5*page.DefaultSize : 5*page.DefaultSize+page.HeaderSize])
		rows, sum := scan(t, b, recover.Options{Columns: cols, Remnants: true})
		if sum.InvalidPages != 1 {
			t.Fatalf("invalid pages = %d", sum.InvalidPages)
		}
		onPage := 0
		for _, r := range rows {
			if r.Block == 5 {
				onPage++
			}
		}
		if onPage < 50 {
			t.Fatalf("only %d tuples salvaged from the page with a destroyed header", onPage)
		}
	})
	t.Run("torn page (halves of two blocks)", func(t *testing.T) {
		b := append([]byte(nil), good...)
		copy(b[2*page.DefaultSize+4096:3*page.DefaultSize], good[7*page.DefaultSize+4096:8*page.DefaultSize])
		_, sum := scan(t, b, recover.Options{Columns: cols})
		if sum.ChecksumFailures != 1 {
			t.Fatalf("torn page not detected: %+v", sum)
		}
	})
	t.Run("line pointer past the page", func(t *testing.T) {
		b := append([]byte(nil), good...)
		b[page.HeaderSize+2] = 0xFF // lp_len high bits of item 1
		b[page.HeaderSize+3] = 0xFF
		_, sum := scan(t, b, recover.Options{Columns: cols})
		if sum.InvalidPages == 0 {
			t.Fatal("bad line pointer not reported")
		}
	})
}

func FuzzScanPage(f *testing.F) {
	f.Add(fixture.Bytes(f, "stages/A-intact/accounts.heap")[:page.DefaultSize])
	cols, _ := schema.Parse("id int4, owner text, balance numeric(12,2), opened timestamptz, memo text")
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) != page.DefaultSize {
			return
		}
		recover.ScanPage(b, 0, recover.Options{Columns: cols, Remnants: true})
	})
}
