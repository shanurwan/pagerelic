package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/shanurwan/pagerelic/internal/buildinfo"
	"github.com/shanurwan/pagerelic/internal/carve"
	"github.com/shanurwan/pagerelic/internal/catalog"
	"github.com/shanurwan/pagerelic/internal/schema"
)

func runRelations(a *app, args []string) int {
	fset := flag.NewFlagSet("relations", flag.ContinueOnError)
	var c common
	c.register(fset)
	dataDir := fset.String("datadir", "", "PostgreSQL data directory")
	dbArg := fset.String("db", "", "database name or OID (omit to list databases)")
	dropped := fset.Bool("dropped", false, "include relations whose catalog rows were deleted (dropped tables)")
	asJSON := fset.Bool("json", false, "print JSON")
	if _, ok := a.parse(fset, &c, args, ""); !ok {
		return ExitUsage
	}
	if *dataDir == "" {
		return a.usageErr(fset, "--datadir is required")
	}
	xl, err := loadXact(*dataDir, "")
	if err != nil {
		a.log.Warn("commit log unavailable", "err", err)
		xl = nil
	}
	if *dbArg == "" {
		dbs, err := catalog.Databases(*dataDir, xl)
		if err != nil {
			return a.fail(err)
		}
		if *asJSON {
			json.NewEncoder(a.stdout).Encode(dbs)
			return ExitOK
		}
		tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "OID\tDATABASE\tDIRECTORY")
		for _, d := range dbs {
			fmt.Fprintf(tw, "%d\t%s\t%s\n", d.OID, d.Name, d.Dir)
		}
		tw.Flush()
		return ExitOK
	}
	db, err := openCatalog(*dataDir, *dbArg, xl)
	if err != nil {
		return a.fail(err)
	}
	rels := db.Relations(*dropped)
	if *asJSON {
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rels)
		return ExitOK
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "OID\tRELATION\tKIND\tSTATE\tFILE\tTOAST\tCOLUMNS")
	for _, r := range rels {
		if r.Namespace == "pg_catalog" || r.Namespace == "information_schema" || r.Namespace == "pg_toast" && r.State == "live" {
			continue
		}
		file := "missing"
		if _, err := os.Stat(r.Path); err == nil {
			file = filepath.Base(r.Path)
		}
		fmt.Fprintf(tw, "%d\t%s.%s\t%s\t%s\t%s\t%d\t%s\n", r.OID, r.Namespace, r.Name, r.Kind, r.State, file, r.ToastOID, schema.String(r.Columns))
	}
	tw.Flush()
	return ExitOK
}

func runXact(a *app, args []string) int {
	fset := flag.NewFlagSet("xact", flag.ContinueOnError)
	var c common
	c.register(fset)
	dataDir := fset.String("datadir", "", "PostgreSQL data directory")
	xactDir := fset.String("xact", "", "pg_xact directory")
	pos, ok := a.parse(fset, &c, args, "<xid>...")
	if !ok {
		return ExitUsage
	}
	if len(pos) == 0 || (*dataDir == "" && *xactDir == "") {
		return a.usageErr(fset, "give --datadir or --xact and at least one transaction ID")
	}
	xl, err := loadXact(*dataDir, *xactDir)
	if err != nil {
		return a.fail(err)
	}
	for _, p := range pos {
		x, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return a.usageErr(fset, "bad transaction ID %q", p)
		}
		fmt.Fprintf(a.stdout, "%d\t%s\n", x, xl.Status(uint32(x)))
	}
	return ExitOK
}

func runVersion(a *app, args []string) int {
	fset := flag.NewFlagSet("version", flag.ContinueOnError)
	asJSON := fset.Bool("json", false, "print JSON")
	if _, ok := a.parse(fset, nil, args, ""); !ok {
		return ExitUsage
	}
	bi := buildinfo.Get()
	if *asJSON {
		json.NewEncoder(a.stdout).Encode(bi)
		return ExitOK
	}
	rev := bi.Revision
	if rev == "" {
		rev = "unknown"
	}
	fmt.Fprintf(a.stdout, "pagerelic %s (revision %s, %s, %s/%s)\n", bi.Version, rev, bi.GoVersion, bi.OS, bi.Arch)
	return ExitOK
}

func runCarve(a *app, args []string) int {
	fset := flag.NewFlagSet("carve", flag.ContinueOnError)
	var c common
	c.register(fset)
	out := fset.String("out", "", "output directory (must not exist or be empty)")
	align := fset.Int64("align", 4096, "page start alignment on the medium")
	pageSize := fset.Int("page-size", 8192, "page size (BLCKSZ)")
	offset := fset.Int64("offset", 0, "start of the byte range to scan")
	length := fset.Int64("length", 0, "length of the byte range (default: to the end)")
	maxBlock := fset.Uint("max-block", 131072, "block-number range searched when inferring a page's block from its checksum")
	pos, ok := a.parse(fset, &c, args, "<disk-or-image>")
	if !ok {
		return ExitUsage
	}
	if len(pos) != 1 || *out == "" {
		return a.usageErr(fset, "a source and --out are required")
	}
	if entries, err := os.ReadDir(*out); err == nil && len(entries) > 0 {
		return a.fail(fmt.Errorf("%s exists and is not empty", *out))
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return a.fail(err)
	}
	src, err := os.Open(pos[0])
	if err != nil {
		return a.fail(err)
	}
	defer src.Close()
	size, err := src.Seek(0, io.SeekEnd)
	if err != nil || size == 0 {
		return a.fail(fmt.Errorf("cannot determine the size of %s (on Windows, image raw devices with BareRelic first): %v", pos[0], err))
	}
	end := int64(0)
	if *length > 0 {
		end = *offset + *length
	}
	files := map[string]*os.File{}
	counts := map[string]int{}
	index, err := createExclusive(filepath.Join(*out, "index.jsonl"))
	if err != nil {
		return a.fail(err)
	}
	defer index.Close()
	enc := json.NewEncoder(index)
	st, scanErr := carve.Scan(a.ctx, src, size, carve.Options{PageSize: *pageSize, Align: *align, Start: *offset, End: end, MaxBlock: uint32(*maxBlock)},
		func(p carve.Page, b []byte) error {
			f := files[p.Group]
			if f == nil {
				var err error
				if f, err = createExclusive(filepath.Join(*out, p.Group+".pages")); err != nil {
					return err
				}
				files[p.Group] = f
			}
			if _, err := f.Write(b); err != nil {
				return err
			}
			rec := struct {
				carve.Page
				File      string `json:"file"`
				FileBlock int    `json:"file_block"`
			}{p, p.Group + ".pages", counts[p.Group]}
			counts[p.Group]++
			return enc.Encode(rec)
		})
	var closeErr error
	for _, f := range files {
		closeErr = errors.Join(closeErr, f.Sync(), f.Close())
	}
	if scanErr != nil {
		return a.fail(scanErr)
	}
	if closeErr != nil {
		return a.fail(closeErr)
	}
	fmt.Fprintf(a.stdout, "scanned %d bytes, carved %d pages\n", st.Scanned, st.Pages)
	var groups []string
	for g := range counts {
		groups = append(groups, g)
	}
	for _, g := range groups {
		fmt.Fprintf(a.stdout, "  %-16s %d pages -> %s\n", g, counts[g], filepath.Join(*out, g+".pages"))
	}
	if len(groups) > 0 {
		fmt.Fprintln(a.stdout, "\nDecode a heap group with its schema (from `pagerelic relations --dropped` or known DDL):")
		fmt.Fprintf(a.stdout, "  pagerelic rows --checksums off --remnants --schema \"...\" %s\n", filepath.Join(*out, strings.TrimSpace(groups[0])+".pages"))
	}
	return ExitOK
}
