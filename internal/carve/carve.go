// Package carve finds PostgreSQL pages on raw media (a disk, a partition,
// or an image made with BareRelic) when the files that held them are gone:
// a deleted data directory, a dropped table whose file was unlinked, or a
// file system too damaged to mount.
//
// A candidate must pass every PostgreSQL page-header sanity check, carry a
// line pointer array consistent with its tuples, and, when it has a
// checksum, admit at least one block number that reproduces it.
package carve

import (
	"context"
	"errors"
	"io"
	"sort"

	"github.com/shanurwan/pagerelic/internal/heap"
	"github.com/shanurwan/pagerelic/internal/page"
)

// Options tune a scan.
type Options struct {
	PageSize int
	Align    int64  // page start alignment on the medium (default 4096: file-system block)
	Start    int64  // byte range to scan
	End      int64  // 0 = to the end
	MaxBlock uint32 // search range for checksum block inference (default one 1 GiB segment)
	Progress func(pos int64)
}

// Page is a carved page.
type Page struct {
	Offset     int64     `json:"offset"`
	LSN        string    `json:"lsn"`
	Kind       page.Kind `json:"kind"`
	Items      int       `json:"items"`
	Tuples     int       `json:"tuples"`
	Natts      int       `json:"natts,omitempty"` // most common attribute count (heap pages)
	Checksum   uint16    `json:"checksum"`
	Candidates []uint32  `json:"block_candidates,omitempty"`
	Group      string    `json:"group"`
}

// Stats summarise a scan.
type Stats struct {
	Scanned  int64          `json:"scanned"`
	Pages    int            `json:"pages"`
	ByKind   map[string]int `json:"by_kind"`
	Rejected int            `json:"rejected_candidates"`
}

// Scan reports every plausible page in order of offset.
func Scan(ctx context.Context, r io.ReaderAt, size int64, opt Options, fn func(Page, []byte) error) (Stats, error) {
	if opt.PageSize == 0 {
		opt.PageSize = page.DefaultSize
	}
	if opt.Align <= 0 {
		opt.Align = 4096
	}
	if opt.MaxBlock == 0 {
		opt.MaxBlock = uint32((1 << 30) / opt.PageSize)
	}
	end := opt.End
	if end <= 0 || end > size {
		end = size
	}
	st := Stats{ByKind: map[string]int{}}
	start := opt.Start - opt.Start%opt.Align
	window := min(int64(8<<20), max(end-start, 0)) // no 8 MiB buffer for a small source
	window -= window % opt.Align
	if window == 0 {
		window = opt.Align
	}
	buf := make([]byte, window+int64(opt.PageSize))
	for base := start; base < end; base += window {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		n, err := r.ReadAt(buf[:min(int64(len(buf)), size-base)], base)
		if err != nil && !errors.Is(err, io.EOF) && n == 0 {
			// Unreadable window (bad sectors on a device): skip it.
			continue
		}
		for off := int64(0); off < window && base+off < end && off+int64(opt.PageSize) <= int64(n); off += opt.Align {
			p, ok := examine(buf[off:off+int64(opt.PageSize)], opt)
			if !ok {
				continue
			}
			p.Offset = base + off
			st.Pages++
			st.ByKind[string(p.Kind)]++
			if err := fn(p, buf[off:off+int64(opt.PageSize)]); err != nil {
				return st, err
			}
		}
		st.Scanned = min(base+window, end) - start
		if opt.Progress != nil {
			opt.Progress(min(base+window, end))
		}
	}
	return st, nil
}

func examine(b []byte, opt Options) (Page, bool) {
	if page.IsZero(b[:page.HeaderSize]) {
		return Page{}, false
	}
	h, err := page.ParseHeader(b)
	if err != nil || len(h.Problems(opt.PageSize)) > 0 || h.Upper == 0 {
		return Page{}, false
	}
	items := page.Items(b, h)
	if len(items) == 0 {
		return Page{}, false // empty pages carry nothing to recover
	}
	for _, it := range items {
		if page.ItemProblem(it, h, opt.PageSize) != "" {
			return Page{}, false
		}
	}
	p := Page{LSN: h.LSN.String(), Kind: page.DetectKind(b, h), Items: len(items), Checksum: h.Checksum}
	if p.Kind == page.KindUnknown {
		return Page{}, false
	}
	if p.Kind == page.KindHeap {
		natts := map[int]int{}
		for _, it := range items {
			if it.Flags != page.LPNormal || !it.HasStorage() {
				continue
			}
			th, err := heap.ParseHeader(b[it.Offset : it.Offset+it.Length])
			if err != nil {
				return Page{}, false
			}
			natts[th.Natts()]++
			p.Tuples++
		}
		best := 0
		for n, c := range natts {
			if c > natts[best] || (c == natts[best] && n > best) {
				best = n
			}
		}
		p.Natts = best
	}
	if h.Checksum != 0 {
		p.Candidates = page.CandidateBlocks(page.BlockSum(b), h.Checksum, opt.MaxBlock, 16)
		if len(p.Candidates) == 0 {
			return Page{}, false
		}
	}
	p.Group = GroupKey(p)
	return p, true
}

// GroupKey buckets pages that plausibly belong to the same relation:
// same access method and, for heaps, the same attribute count.
func GroupKey(p Page) string {
	if p.Kind == page.KindHeap {
		return "heap-natts" + itoa(p.Natts)
	}
	return string(p.Kind)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}

// SortByLSN orders pages of one group by WAL position, which approximates
// the order in which they were last written.
func SortByLSN(pages []Page) {
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].LSN < pages[j].LSN })
}
