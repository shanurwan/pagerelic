package heap_test

import (
	"fmt"
	"testing"

	"github.com/shanurwan/pagerelic/internal/fixture"
	"github.com/shanurwan/pagerelic/internal/heap"
	"github.com/shanurwan/pagerelic/internal/page"
	"github.com/shanurwan/pagerelic/internal/xact"
)

type keyItem struct {
	Rel        string  `json:"rel"`
	Blkno      uint32  `json:"blkno"`
	LP         int     `json:"lp"`
	LPOff      int     `json:"lp_off"`
	LPFlags    int     `json:"lp_flags"`
	LPLen      int     `json:"lp_len"`
	TXmin      *int64  `json:"t_xmin"`
	TXmax      *int64  `json:"t_xmax"`
	TField3    *int64  `json:"t_field3"`
	TCtid      *string `json:"t_ctid"`
	TInfomask2 *int    `json:"t_infomask2"`
	TInfomask  *int    `json:"t_infomask"`
	THoff      *int    `json:"t_hoff"`
	TBits      *string `json:"t_bits"`
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// TestItemsMatchHeapPageItems compares every line pointer and tuple header
// with pageinspect's heap_page_items for the same pages.
func TestItemsMatchHeapPageItems(t *testing.T) {
	var key []keyItem
	fixture.JSON(t, "key/items.json", &key)
	files := map[string][]byte{
		"people": fixture.Bytes(t, "raw/base/16388/16434"),
		"blobs":  fixture.Bytes(t, "raw/base/16388/16442"),
	}
	tuples := 0
	for _, k := range key {
		b := files[k.Rel][int(k.Blkno)*page.DefaultSize : int(k.Blkno+1)*page.DefaultSize]
		ph, _ := page.ParseHeader(b)
		items := page.Items(b, ph)
		it := items[k.LP-1]
		if int(it.Offset) != k.LPOff || int(it.Flags) != k.LPFlags || int(it.Length) != k.LPLen {
			t.Fatalf("%s (%d,%d): item %+v, want off=%d flags=%d len=%d", k.Rel, k.Blkno, k.LP, it, k.LPOff, k.LPFlags, k.LPLen)
		}
		if k.TXmin == nil {
			if it.Flags == page.LPNormal {
				t.Fatalf("pageinspect decoded no header for a normal item")
			}
			continue
		}
		h, err := heap.ParseHeader(b[it.Offset : it.Offset+it.Length])
		if err != nil {
			t.Fatalf("%s (%d,%d): %v", k.Rel, k.Blkno, k.LP, err)
		}
		got := []any{int64(h.Xmin), int64(h.Xmax), int64(h.Field3), h.Ctid(), int(h.Infomask2), int(h.Infomask), int(h.Hoff)}
		want := []any{deref(k.TXmin), deref(k.TXmax), deref(k.TField3), deref(k.TCtid), deref(k.TInfomask2), deref(k.TInfomask), deref(k.THoff)}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s (%d,%d):\n got %v\nwant %v", k.Rel, k.Blkno, k.LP, got, want)
		}
		wantBits := ""
		if k.TBits != nil {
			wantBits = *k.TBits
		}
		if h.BitString() != wantBits {
			t.Fatalf("%s (%d,%d): bits %q, want %q", k.Rel, k.Blkno, k.LP, h.BitString(), wantBits)
		}
		tuples++
	}
	if tuples < 300 {
		t.Fatalf("only %d tuples compared", tuples)
	}
}

type keyXid struct {
	Xid    uint32 `json:"xid"`
	Status string `json:"status"`
}

func TestXactMatchesPgXactStatus(t *testing.T) {
	var key []keyXid
	fixture.JSON(t, "key/xids.json", &key)
	log := xact.FromSegment(0, fixture.Bytes(t, "raw/pg_xact/0000"))
	if len(key) == 0 {
		t.Fatal("empty xid key")
	}
	for _, k := range key {
		want := map[string]string{"committed": "committed", "aborted": "aborted", "in progress": "in-progress"}[k.Status]
		if got := log.Status(k.Xid).String(); got != want {
			t.Errorf("xid %d: %s, pg_xact_status says %s", k.Xid, got, k.Status)
		}
	}
}

// TestStagesClassification checks MVCC classification on the controlled
// experiment (stage A: after DELETE / UPDATE / ROLLBACK, before any read).
func TestStagesClassification(t *testing.T) {
	file := fixture.Bytes(t, "stages/A-intact/accounts.heap")
	log := xact.FromSegment(0, fixture.Bytes(t, "stages/A-intact/pg_xact_0000"))
	counts := map[heap.State]int{}
	for blk := 0; blk*page.DefaultSize < len(file); blk++ {
		b := file[blk*page.DefaultSize : (blk+1)*page.DefaultSize]
		ph, _ := page.ParseHeader(b)
		for _, it := range page.Items(b, ph) {
			if it.Flags != page.LPNormal {
				continue
			}
			h, err := heap.ParseHeader(b[it.Offset : it.Offset+it.Length])
			if err != nil {
				t.Fatal(err)
			}
			v := heap.Classify(h, uint32(blk), uint16(it.Index), log)
			counts[v.State]++
			// Without the commit log, tuples lacking hint bits must be unknown, never guessed.
			if nv := heap.Classify(h, uint32(blk), uint16(it.Index), nil); nv.State != v.State && nv.State != heap.Unknown {
				t.Fatalf("hint-only verdict %s contradicts pg_xact verdict %s", nv.State, v.State)
			}
		}
	}
	// 1000 inserted; 200 deleted; 89 non-deleted rows updated (new versions live);
	// 5 aborted. Pruning by the UPDATE already removed some dead tuples, so
	// only the totals that must hold are asserted.
	if counts[heap.Live] != 800 {
		t.Errorf("live = %d, want 800 (counts %v)", counts[heap.Live], counts)
	}
	if counts[heap.Unknown] != 0 || counts[heap.InProgress] != 0 {
		t.Errorf("unexpected unresolved states: %v", counts)
	}
	t.Logf("stage A tuple states: %v", counts)
}

func FuzzParseHeader(f *testing.F) {
	b := fixture.Bytes(f, "raw/base/16388/16434")[:page.DefaultSize]
	ph, _ := page.ParseHeader(b)
	it := page.Items(b, ph)[0]
	f.Add(b[it.Offset : it.Offset+it.Length])
	f.Fuzz(func(t *testing.T, b []byte) {
		h, err := heap.ParseHeader(b)
		if err != nil {
			return
		}
		_ = h.FlagNames()
		_ = h.BitString()
		for i := 0; i < h.Natts()+3; i++ {
			_ = h.IsNull(i)
		}
		_ = heap.Classify(h, 0, 1, nil)
	})
}
