package recover

import (
	"context"
	"fmt"
	"sort"

	"github.com/shanurwan/pagerelic/internal/heap"
	"github.com/shanurwan/pagerelic/internal/page"
	"github.com/shanurwan/pagerelic/internal/relation"
	"github.com/shanurwan/pagerelic/internal/schema"
	"github.com/shanurwan/pagerelic/internal/toast"
)

// ChecksumMode controls page checksum verification.
type ChecksumMode int

const (
	ChecksumAuto ChecksumMode = iota // verify pages whose pd_checksum is non-zero
	ChecksumOn                       // treat a zero checksum as a failure
	ChecksumOff                      // never verify (e.g. carved pages of unknown block number)
)

// Options configure a scan.
type Options struct {
	Columns   []schema.Column
	Xact      heap.XactLookup
	Toast     *toast.Store
	Checksums ChecksumMode
	// Remnants carves tuples out of free space and unreferenced gaps. Pages
	// whose header is unusable are always carved when this is set.
	Remnants bool
}

// Source says how a row was found.
const (
	SourceLinePointer = "line-pointer"
	SourceRemnant     = "remnant"
)

// Confidence grades.
const (
	High   = "high"
	Medium = "medium"
	Low    = "low"
)

// Row is one recovered tuple version.
type Row struct {
	Block      uint32     `json:"block"`
	Item       int        `json:"lp,omitempty"`
	Offset     int        `json:"offset"`
	Length     int        `json:"length"`
	Source     string     `json:"source"`
	State      heap.State `json:"state"`
	Evidence   string     `json:"evidence,omitempty"`
	Xmin       uint32     `json:"xmin"`
	Xmax       uint32     `json:"xmax"`
	Ctid       string     `json:"ctid"`
	Flags      []string   `json:"flags,omitempty"`
	Confidence string     `json:"confidence"`
	Notes      []string   `json:"notes,omitempty"`
	Values     []Value    `json:"values"`
}

// PageReport summarises one page.
type PageReport struct {
	Block     uint32    `json:"block"`
	LSN       string    `json:"lsn"`
	Kind      page.Kind `json:"kind"`
	Checksum  string    `json:"checksum"` // ok, mismatch, none, new, not-checked
	Problems  []string  `json:"problems,omitempty"`
	Items     int       `json:"items"`
	Tuples    int       `json:"tuples"`
	Remnants  int       `json:"remnants"`
	Stale     int       `json:"stale_copies,omitempty"`
	FreeBytes int       `json:"free_bytes"`
}

// Summary totals a scan.
type Summary struct {
	Pages            int                `json:"pages"`
	ChecksumFailures int                `json:"checksum_failures"`
	InvalidPages     int                `json:"invalid_pages"`
	Rows             int                `json:"rows"`
	Remnants         int                `json:"remnants"`
	ByState          map[heap.State]int `json:"by_state"`
	DecodeErrors     int                `json:"rows_with_decode_errors"`
	PartialTailBytes int64              `json:"partial_tail_bytes,omitempty"`
}

// Scan walks every page of rel. onPage and onRow may be nil.
func Scan(ctx context.Context, rel *relation.Relation, opt Options, onPage func(PageReport), onRow func(Row) error) (Summary, error) {
	sum := Summary{ByState: map[heap.State]int{}, PartialTailBytes: rel.Partial}
	buf := make([]byte, rel.PageSize)
	for blk := uint32(0); blk < rel.Blocks(); blk++ {
		if blk%256 == 0 {
			if err := ctx.Err(); err != nil {
				return sum, err
			}
		}
		if err := rel.ReadPage(blk, buf); err != nil {
			return sum, err
		}
		rep, rows := ScanPage(buf, blk, opt)
		sum.Pages++
		if rep.Checksum == "mismatch" {
			sum.ChecksumFailures++
		}
		if len(rep.Problems) > 0 {
			sum.InvalidPages++
		}
		sum.Remnants += rep.Remnants
		if onPage != nil {
			onPage(rep)
		}
		for _, r := range rows {
			sum.Rows++
			sum.ByState[r.State]++
			if decodeErrors(r.Values) > 0 {
				sum.DecodeErrors++
			}
			if onRow != nil {
				if err := onRow(r); err != nil {
					return sum, err
				}
			}
		}
	}
	return sum, nil
}

// ScanPage decodes one page. It never panics on malformed input.
func ScanPage(b []byte, blk uint32, opt Options) (PageReport, []Row) {
	size := len(b)
	rep := PageReport{Block: blk}
	if page.IsZero(b) {
		rep.Kind, rep.Checksum = page.KindNew, "new"
		return rep, nil
	}
	h, _ := page.ParseHeader(b)
	rep.LSN = h.LSN.String()
	rep.Problems = h.Problems(size)
	headerOK := len(rep.Problems) == 0
	if headerOK {
		rep.Kind = page.DetectKind(b, h)
		rep.FreeBytes = int(h.Upper) - int(h.Lower)
	} else {
		rep.Kind = page.KindUnknown
	}

	checksumOK := true
	switch {
	case opt.Checksums == ChecksumOff:
		rep.Checksum = "not-checked"
	case h.Checksum == 0 && opt.Checksums == ChecksumAuto:
		rep.Checksum = "none"
	default:
		if page.Checksum(b, blk) == h.Checksum {
			rep.Checksum = "ok"
		} else {
			rep.Checksum, checksumOK = "mismatch", false
			rep.Problems = append(rep.Problems, fmt.Sprintf("checksum mismatch: stored %d, computed %d for block %d", h.Checksum, page.Checksum(b, blk), blk))
		}
	}
	if headerOK && rep.Kind != page.KindHeap {
		return rep, nil // indexes and other access methods are reported, not decoded
	}

	var rows []Row
	type span struct{ lo, hi int }
	var covered []span
	lpXmins := map[[2]uint32]int{} // (xmin, ctid) → item, to spot stale copies
	if headerOK {
		items := page.Items(b, h)
		rep.Items = len(items)
		for _, it := range items {
			if msg := page.ItemProblem(it, h, size); msg != "" {
				rep.Problems = append(rep.Problems, msg)
				continue
			}
			if it.Flags == page.LPRedirect || !it.HasStorage() {
				continue
			}
			lo, hi := int(it.Offset), int(it.Offset)+int(it.Length)
			covered = append(covered, span{lo, hi})
			row, ok := decodeAt(b, lo, hi, blk, it.Index, opt)
			row.Source = SourceLinePointer
			if !ok {
				row.Confidence = Low
				row.Notes = append(row.Notes, "tuple does not fully match the column layout")
			} else if checksumOK {
				row.Confidence = High
			} else {
				row.Confidence = Low
			}
			if !checksumOK {
				row.Notes = append(row.Notes, "page checksum mismatch: content may be torn or corrupted")
			}
			if it.Flags == page.LPDead {
				row.Notes = append(row.Notes, "line pointer marked dead")
			}
			if th, err := heap.ParseHeader(b[lo:hi]); err == nil {
				// A remnant is a stale copy of this tuple if it has the same
				// inserting transaction and either the current ctid or the
				// tuple's own location (the ctid it had before an update).
				lpXmins[[2]uint32{th.Xmin, th.CtidBlock<<16 | uint32(th.CtidOffset)}] = it.Index
				lpXmins[[2]uint32{th.Xmin, blk<<16 | uint32(it.Index)}] = it.Index
			}
			rows = append(rows, row)
			rep.Tuples++
		}
	}

	if opt.Remnants {
		var regions []span
		if headerOK {
			regions = append(regions, span{int(h.Lower), int(h.Upper)})
			sort.Slice(covered, func(i, j int) bool { return covered[i].lo < covered[j].lo })
			pos := int(h.Upper)
			for _, c := range covered {
				if c.lo > pos {
					regions = append(regions, span{pos, c.lo})
				}
				pos = max(pos, c.hi)
			}
			if pos < int(h.Special) {
				regions = append(regions, span{pos, int(h.Special)})
			}
		} else {
			regions = append(regions, span{page.HeaderSize, size})
		}
		items := []page.ItemID{}
		if headerOK {
			items = page.Items(b, h)
		}
		for _, rg := range regions {
			for off := alignUp(rg.lo); off+heap.HeaderSize+1 <= rg.hi; off += page.MaxAlign {
				row, end, ok := carveAt(b, off, rg.hi, blk, items, opt)
				if !ok {
					continue
				}
				key := [2]uint32{row.Xmin, ctidKey(b[off:])}
				if lp, dup := lpXmins[key]; dup {
					rep.Stale++
					_ = lp
					off = alignUp(end) - page.MaxAlign
					continue
				}
				if !checksumOK {
					row.Notes = append(row.Notes, "page checksum mismatch")
				}
				rows = append(rows, row)
				rep.Remnants++
				off = alignUp(end) - page.MaxAlign
			}
		}
	}
	return rep, rows
}

func alignUp(n int) int { return (n + page.MaxAlign - 1) &^ (page.MaxAlign - 1) }

func ctidKey(b []byte) uint32 {
	h, err := heap.ParseHeader(b)
	if err != nil {
		return 0
	}
	return h.CtidBlock<<16 | uint32(h.CtidOffset)
}

// decodeAt decodes the tuple in b[lo:hi] referenced by a line pointer.
func decodeAt(b []byte, lo, hi int, blk uint32, item int, opt Options) (Row, bool) {
	row := Row{Block: blk, Item: item, Offset: lo, Length: hi - lo}
	h, err := heap.ParseHeader(b[lo:hi])
	if err != nil {
		row.State = heap.Unknown
		row.Notes = append(row.Notes, err.Error())
		return row, false
	}
	fillHeader(&row, h, heap.Classify(h, blk, uint16(item), opt.Xact))
	vals, end, err := DecodeValues(b[lo:hi], h, opt.Columns, opt.Toast)
	row.Values = padValues(vals, opt.Columns)
	if err != nil {
		row.Notes = append(row.Notes, err.Error())
		return row, false
	}
	if alignUp(end) < hi-lo && end < hi-lo {
		row.Notes = append(row.Notes, fmt.Sprintf("%d trailing bytes after the last attribute", hi-lo-end))
	}
	return row, decodeErrors(row.Values) == 0
}

// carveAt tries to read a plausible tuple at b[off:limit] that no line
// pointer references. Acceptance is strict: a valid header, the schema's
// attribute count, a plausible xmin, and every attribute decoding cleanly.
func carveAt(b []byte, off, limit int, blk uint32, items []page.ItemID, opt Options) (Row, int, bool) {
	h, err := heap.ParseHeader(b[off:limit])
	if err != nil || h.Natts() == 0 || h.Natts() > len(opt.Columns) || h.Natts() < len(opt.Columns)-8 {
		return Row{}, 0, false
	}
	if h.Xmin < 3 && h.Xmin != heap.FrozenXID {
		return Row{}, 0, false
	}
	if h.Infomask&(heap.XmaxCommitted|heap.XmaxInvalid) == heap.XmaxCommitted|heap.XmaxInvalid {
		return Row{}, 0, false
	}
	if h.Infomask&(heap.MovedOff|heap.MovedIn) != 0 {
		return Row{}, 0, false
	}
	vals, end, err := DecodeValues(b[off:limit], h, opt.Columns, opt.Toast)
	if err != nil || decodeErrors(vals) > 0 {
		return Row{}, 0, false
	}
	// A deleted tuple's ctid points at itself; after pruning its line pointer
	// is unused or dead. Use that to tell "deleted" from "updated".
	selfItem := uint16(0)
	if h.CtidBlock == blk {
		idx := int(h.CtidOffset)
		if idx >= 1 && idx <= len(items) && items[idx-1].Flags != page.LPNormal {
			selfItem = h.CtidOffset
		} else if idx > len(items) {
			selfItem = h.CtidOffset
		}
	}
	row := Row{Block: blk, Offset: off, Length: end, Source: SourceRemnant, Confidence: Medium,
		Notes: []string{"reconstructed from page free space: no line pointer references it"}}
	fillHeader(&row, h, heap.Classify(h, blk, selfItem, opt.Xact))
	if row.State == heap.Live {
		// Nothing references this tuple, so it cannot be the current row.
		row.State = heap.Superseded
		row.Notes = append(row.Notes, "header looks live but no line pointer references it: an older image of a row that was later updated or deleted")
	}
	row.Values = padValues(vals, opt.Columns)
	return row, off + end, true
}

func fillHeader(r *Row, h heap.Header, v heap.Verdict) {
	r.Xmin, r.Xmax, r.Ctid, r.Flags = h.Xmin, h.Xmax, h.Ctid(), h.FlagNames()
	r.State, r.Evidence = v.State, v.Evidence
}

func padValues(vals []Value, cols []schema.Column) []Value {
	n := len(schema.Visible(cols))
	for len(vals) < n {
		vals = append(vals, Value{Err: "not decoded: an earlier column is corrupt"})
	}
	return vals
}
