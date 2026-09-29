package page

import (
	"testing"

	"github.com/shanurwan/pagerelic/internal/fixture"
)

// keyPage is one row of pageinspect's page_header() plus page_checksum().
type keyPage struct {
	Rel              string `json:"rel"`
	Blkno            uint32 `json:"blkno"`
	LSN              string `json:"lsn"`
	Checksum         int    `json:"checksum"`
	Flags            int    `json:"flags"`
	Lower            int    `json:"lower"`
	Upper            int    `json:"upper"`
	Special          int    `json:"special"`
	PageSize         int    `json:"pagesize"`
	Version          int    `json:"version"`
	PruneXID         int64  `json:"prune_xid"`
	ComputedChecksum int    `json:"computed_checksum"`
}

var relFiles = map[string]string{"people": "raw/base/16388/16434", "blobs": "raw/base/16388/16442"}

// TestHeaderAndChecksumMatchPostgreSQL compares every page against
// PostgreSQL 17's own pageinspect output for the same bytes.
func TestHeaderAndChecksumMatchPostgreSQL(t *testing.T) {
	var key []keyPage
	fixture.JSON(t, "key/pages.json", &key)
	files := map[string][]byte{}
	for rel, path := range relFiles {
		files[rel] = fixture.Bytes(t, path)
	}
	if len(key) < 10 {
		t.Fatalf("answer key has only %d pages", len(key))
	}
	for _, k := range key {
		b := files[k.Rel][int(k.Blkno)*DefaultSize : int(k.Blkno+1)*DefaultSize]
		h, err := ParseHeader(b)
		if err != nil {
			t.Fatal(err)
		}
		// pageinspect reads the shared-buffer copy, whose pd_checksum is not
		// maintained (PostgreSQL stamps checksums on the copy it writes out),
		// so the on-disk stored checksum is compared with PostgreSQL's
		// page_checksum() instead of page_header().checksum.
		if int(int16(h.Checksum)) != k.ComputedChecksum {
			t.Errorf("%s block %d: stored checksum %d, PostgreSQL computes %d", k.Rel, k.Blkno, int16(h.Checksum), k.ComputedChecksum)
		}
		k.Checksum = int(int16(h.Checksum))
		got := keyPage{Rel: k.Rel, Blkno: k.Blkno, LSN: h.LSN.String(), Checksum: int(int16(h.Checksum)), Flags: int(h.Flags),
			Lower: int(h.Lower), Upper: int(h.Upper), Special: int(h.Special), PageSize: h.Size, Version: int(h.Version),
			PruneXID: int64(h.PruneXID), ComputedChecksum: int(int16(Checksum(b, k.Blkno)))}
		if got != k {
			t.Errorf("%s block %d:\n got %+v\nwant %+v", k.Rel, k.Blkno, got, k)
		}
		if p := h.Problems(DefaultSize); len(p) != 0 {
			t.Errorf("%s block %d: valid page reported problems %v", k.Rel, k.Blkno, p)
		}
		if kind := DetectKind(b, h); kind != KindHeap {
			t.Errorf("%s block %d: kind %s", k.Rel, k.Blkno, kind)
		}
	}
}

func TestChecksumDetectsSingleBitFlipAndWrongBlock(t *testing.T) {
	b := append([]byte(nil), fixture.Bytes(t, relFiles["people"])[:DefaultSize]...)
	h, _ := ParseHeader(b)
	if Checksum(b, 0) != h.Checksum {
		t.Fatal("baseline checksum mismatch")
	}
	if Checksum(b, 1) == h.Checksum {
		t.Fatal("checksum must depend on the block number")
	}
	for _, off := range []int{30, 4000, DefaultSize - 1} {
		c := append([]byte(nil), b...)
		c[off] ^= 0x01
		if Checksum(c, 0) == h.Checksum {
			t.Errorf("bit flip at %d not detected", off)
		}
	}
	// The stored checksum field itself must not influence the computation.
	c := append([]byte(nil), b...)
	c[8], c[9] = 0xAB, 0xCD
	if Checksum(c, 0) != h.Checksum {
		t.Fatal("pd_checksum must be excluded from the computation")
	}
}

func TestCandidateBlocksIncludesTruth(t *testing.T) {
	file := fixture.Bytes(t, relFiles["people"])
	for blk := uint32(0); blk < 14; blk++ {
		b := file[int(blk)*DefaultSize : int(blk+1)*DefaultSize]
		h, _ := ParseHeader(b)
		cands := CandidateBlocks(BlockSum(b), h.Checksum, 1<<17, 1<<10)
		found := false
		for _, c := range cands {
			found = found || c == blk
		}
		if !found {
			t.Fatalf("block %d not among candidates %v", blk, cands)
		}
	}
}

func TestProblemsAndItems(t *testing.T) {
	b := append([]byte(nil), fixture.Bytes(t, relFiles["people"])[:DefaultSize]...)
	h, _ := ParseHeader(b)
	items := Items(b, h)
	if len(items) != (int(h.Lower)-HeaderSize)/4 {
		t.Fatalf("items = %d", len(items))
	}
	for _, it := range items {
		if msg := ItemProblem(it, h, DefaultSize); msg != "" {
			t.Errorf("valid item flagged: %s", msg)
		}
	}
	bad := h
	bad.Lower, bad.Upper, bad.Special, bad.Flags = 10, 9000, 8190, 0x80
	if len(bad.Problems(DefaultSize)) < 4 {
		t.Fatalf("problems = %v", bad.Problems(DefaultSize))
	}
}

func FuzzHeaderAndItems(f *testing.F) {
	f.Add(fixtureSeed(f))
	f.Fuzz(func(t *testing.T, b []byte) {
		h, err := ParseHeader(b)
		if err != nil {
			return
		}
		_ = h.Problems(DefaultSize)
		_ = DetectKind(b, h)
		for _, it := range Items(b, h) {
			_ = ItemProblem(it, h, DefaultSize)
		}
		if len(b) >= DefaultSize {
			_ = Checksum(b[:DefaultSize], 0)
		}
	})
}

func fixtureSeed(f *testing.F) []byte {
	return fixture.Bytes(f, relFiles["people"])[:DefaultSize]
}
