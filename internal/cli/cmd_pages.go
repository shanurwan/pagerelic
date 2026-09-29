package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/shanurwan/pagerelic/internal/heap"
	"github.com/shanurwan/pagerelic/internal/page"
	"github.com/shanurwan/pagerelic/internal/relation"
)

func runInspect(a *app, args []string) int {
	fset := flag.NewFlagSet("inspect", flag.ContinueOnError)
	var c common
	c.register(fset)
	block := fset.Int("block", -1, "show one block's line pointers and tuple headers")
	asJSON := fset.Bool("json", false, "print JSON")
	pageSize := fset.Int("page-size", 8192, "page size (BLCKSZ)")
	segno := fset.Int("segment", -1, "segment number for checksum block numbers (default: from the .N suffix)")
	pos, ok := a.parse(fset, &c, args, "<relation-file>")
	if !ok {
		return ExitUsage
	}
	if len(pos) != 1 {
		return a.usageErr(fset, "exactly one relation file")
	}
	rel, err := relation.Open(pos[0], *pageSize)
	if err != nil {
		return a.fail(err)
	}
	defer rel.Close()
	base := segmentBase(pos[0], *segno, *pageSize)
	buf := make([]byte, *pageSize)

	type itemOut struct {
		LP      int      `json:"lp"`
		State   string   `json:"lp_state"`
		Offset  uint16   `json:"offset"`
		Length  uint16   `json:"length"`
		Problem string   `json:"problem,omitempty"`
		Xmin    uint32   `json:"xmin,omitempty"`
		Xmax    uint32   `json:"xmax,omitempty"`
		Ctid    string   `json:"ctid,omitempty"`
		Natts   int      `json:"natts,omitempty"`
		Flags   []string `json:"flags,omitempty"`
	}
	type pageOut struct {
		Block    uint32    `json:"block"`
		LSN      string    `json:"lsn"`
		Kind     page.Kind `json:"kind"`
		Checksum string    `json:"checksum"`
		Stored   uint16    `json:"stored_checksum"`
		Lower    uint16    `json:"lower"`
		Upper    uint16    `json:"upper"`
		Special  uint16    `json:"special"`
		Flags    []string  `json:"flags,omitempty"`
		Items    int       `json:"items"`
		Problems []string  `json:"problems,omitempty"`
		ItemList []itemOut `json:"line_pointers,omitempty"`
	}
	var pages []pageOut
	bad := false
	for blk := uint32(0); blk < rel.Blocks(); blk++ {
		if *block >= 0 && blk != uint32(*block) {
			continue
		}
		if err := rel.ReadPage(blk, buf); err != nil {
			return a.fail(err)
		}
		p := pageOut{Block: blk}
		if page.IsZero(buf) {
			p.Kind, p.Checksum = page.KindNew, "new"
			pages = append(pages, p)
			continue
		}
		h, _ := page.ParseHeader(buf)
		p.LSN, p.Stored, p.Lower, p.Upper, p.Special, p.Flags = h.LSN.String(), h.Checksum, h.Lower, h.Upper, h.Special, h.FlagNames()
		p.Problems = h.Problems(*pageSize)
		headerOK := len(p.Problems) == 0
		p.Kind = page.KindUnknown
		if headerOK {
			p.Kind = page.DetectKind(buf, h)
		}
		switch {
		case h.Checksum == 0:
			p.Checksum = "none"
		case page.Checksum(buf, base+blk) == h.Checksum:
			p.Checksum = "ok"
		default:
			p.Checksum = "MISMATCH"
			p.Problems = append(p.Problems, fmt.Sprintf("checksum: stored %d, computed %d", h.Checksum, page.Checksum(buf, base+blk)))
		}
		if headerOK { // show line pointers even when the checksum fails: that is when you need them
			items := page.Items(buf, h)
			p.Items = len(items)
			if *block >= 0 {
				for _, it := range items {
					io := itemOut{LP: it.Index, State: it.FlagName(), Offset: it.Offset, Length: it.Length, Problem: page.ItemProblem(it, h, *pageSize)}
					if io.Problem == "" && it.HasStorage() && p.Kind == page.KindHeap {
						if th, err := heap.ParseHeader(buf[it.Offset : it.Offset+it.Length]); err == nil {
							io.Xmin, io.Xmax, io.Ctid, io.Natts, io.Flags = th.Xmin, th.Xmax, th.Ctid(), th.Natts(), th.FlagNames()
						} else {
							io.Problem = err.Error()
						}
					}
					p.ItemList = append(p.ItemList, io)
				}
			}
		}
		bad = bad || len(p.Problems) > 0
		pages = append(pages, p)
	}
	if *asJSON {
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		enc.Encode(pages)
	} else {
		tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "BLOCK\tLSN\tKIND\tCHECKSUM\tLOWER\tUPPER\tSPECIAL\tITEMS\tFLAGS\tPROBLEMS")
		for _, p := range pages {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%s\t%s\n", p.Block, p.LSN, p.Kind, p.Checksum, p.Lower, p.Upper, p.Special, p.Items,
				strings.Join(p.Flags, ","), strings.Join(p.Problems, "; "))
		}
		tw.Flush()
		for _, p := range pages {
			if len(p.ItemList) == 0 {
				continue
			}
			fmt.Fprintf(a.stdout, "\nblock %d line pointers:\n", p.Block)
			tw = tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "LP\tSTATE\tOFFSET\tLEN\tXMIN\tXMAX\tCTID\tNATTS\tFLAGS\tPROBLEM")
			for _, it := range p.ItemList {
				fmt.Fprintf(tw, "%d\t%s\t%d\t%d\t%d\t%d\t%s\t%d\t%s\t%s\n", it.LP, it.State, it.Offset, it.Length, it.Xmin, it.Xmax, it.Ctid, it.Natts, strings.Join(it.Flags, ","), it.Problem)
			}
			tw.Flush()
		}
	}
	if bad {
		return ExitPartial
	}
	return ExitOK
}

// segmentBase returns the absolute block number of a segment's first page.
func segmentBase(path string, segno, pageSize int) uint32 {
	if segno < 0 {
		segno = 0
		if i := strings.LastIndexByte(filepath.Base(path), '.'); i >= 0 {
			if n, err := strconv.Atoi(filepath.Base(path)[i+1:]); err == nil {
				segno = n
			}
		}
	}
	return uint32(segno) * uint32(relation.SegmentSize/pageSize)
}

var relFileRE = regexp.MustCompile(`^\d+(_(fsm|vm|init))?(\.\d+)?$`)

func runVerify(a *app, args []string) int {
	fset := flag.NewFlagSet("verify", flag.ContinueOnError)
	var c common
	c.register(fset)
	asJSON := fset.Bool("json", false, "print JSON")
	pageSize := fset.Int("page-size", 8192, "page size (BLCKSZ)")
	requireChecksums := fset.Bool("require-checksums", false, "treat pages without a checksum as failures")
	pos, ok := a.parse(fset, &c, args, "<file-or-directory>...")
	if !ok {
		return ExitUsage
	}
	if len(pos) == 0 {
		return a.usageErr(fset, "give relation files or a data directory")
	}
	var files []string
	for _, p := range pos {
		st, err := os.Stat(p)
		if err != nil {
			return a.fail(err)
		}
		if !st.IsDir() {
			files = append(files, p)
			continue
		}
		filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				a.log.Warn("unreadable", "path", path, "err", err)
				return nil
			}
			rel, _ := filepath.Rel(p, path)
			top := strings.Split(filepath.ToSlash(rel), "/")[0]
			if d.IsDir() && path != p && top != "base" && top != "global" && top != "pg_tblspc" {
				return fs.SkipDir
			}
			if !d.IsDir() && relFileRE.MatchString(d.Name()) {
				files = append(files, path)
			}
			return nil
		})
	}
	sort.Strings(files)

	type failure struct {
		File    string `json:"file"`
		Block   uint32 `json:"block"`
		Problem string `json:"problem"`
	}
	var fails []failure
	pages, checked := 0, 0
	buf := make([]byte, *pageSize)
	for _, f := range files {
		if err := a.ctx.Err(); err != nil {
			return a.fail(err)
		}
		fh, err := os.Open(f)
		if err != nil {
			fails = append(fails, failure{File: f, Problem: err.Error()})
			continue
		}
		st, _ := fh.Stat()
		base := segmentBase(f, -1, *pageSize)
		if st.Size()%int64(*pageSize) != 0 {
			fails = append(fails, failure{File: f, Problem: fmt.Sprintf("size %d is not a multiple of the page size (torn extension?)", st.Size())})
		}
		for blk := uint32(0); int64(blk+1)*int64(*pageSize) <= st.Size(); blk++ {
			if _, err := fh.ReadAt(buf, int64(blk)*int64(*pageSize)); err != nil {
				fails = append(fails, failure{File: f, Block: blk, Problem: "read error: " + err.Error()})
				continue
			}
			pages++
			if page.IsZero(buf) {
				continue
			}
			h, _ := page.ParseHeader(buf)
			for _, prob := range h.Problems(*pageSize) {
				fails = append(fails, failure{File: f, Block: blk, Problem: prob})
			}
			switch {
			case h.Checksum == 0 && *requireChecksums:
				fails = append(fails, failure{File: f, Block: blk, Problem: "no checksum"})
			case h.Checksum != 0:
				checked++
				if got := page.Checksum(buf, base+blk); got != h.Checksum {
					fails = append(fails, failure{File: f, Block: blk, Problem: fmt.Sprintf("checksum mismatch: stored %d, computed %d", h.Checksum, got)})
				}
			}
		}
		fh.Close()
	}
	if *asJSON {
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{"files": len(files), "pages": pages, "checksums_verified": checked, "failures": fails})
	} else {
		for _, f := range fails {
			fmt.Fprintf(a.stdout, "FAIL  %s  block %d  %s\n", f.File, f.Block, f.Problem)
		}
		fmt.Fprintf(a.stdout, "files %d  pages %d  checksums verified %d  failures %d\n", len(files), pages, checked, len(fails))
	}
	if len(fails) > 0 {
		return ExitPartial
	}
	return ExitOK
}
