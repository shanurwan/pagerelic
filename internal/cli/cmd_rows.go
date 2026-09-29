package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shanurwan/pagerelic/internal/buildinfo"
	"github.com/shanurwan/pagerelic/internal/catalog"
	"github.com/shanurwan/pagerelic/internal/heap"
	"github.com/shanurwan/pagerelic/internal/recover"
	"github.com/shanurwan/pagerelic/internal/relation"
	"github.com/shanurwan/pagerelic/internal/schema"
	"github.com/shanurwan/pagerelic/internal/toast"
	"github.com/shanurwan/pagerelic/internal/xact"
)

// openCatalog resolves --datadir/--db into a bootstrapped database.
func openCatalog(dataDir, dbArg string, xl heap.XactLookup) (*catalog.DB, error) {
	if dbArg == "" {
		return nil, errors.New("--db is required with --datadir (run `pagerelic relations --datadir DIR` to list databases)")
	}
	dir := ""
	if oid, err := strconv.ParseUint(dbArg, 10, 32); err == nil {
		dir = filepath.Join(dataDir, "base", strconv.FormatUint(oid, 10))
	} else {
		dbs, err := catalog.Databases(dataDir, xl)
		if err != nil {
			return nil, err
		}
		for _, d := range dbs {
			if d.Name == dbArg {
				dir = d.Dir
			}
		}
		if dir == "" {
			return nil, fmt.Errorf("database %q not found", dbArg)
		}
	}
	return catalog.Open(dir, xl)
}

func loadXact(dataDir, xactDir string) (heap.XactLookup, error) {
	if xactDir == "" && dataDir != "" {
		xactDir = filepath.Join(dataDir, "pg_xact")
	}
	if xactDir == "" {
		return nil, nil
	}
	return xact.Open(xactDir)
}

func runRows(a *app, args []string) int {
	fs := flag.NewFlagSet("rows", flag.ContinueOnError)
	var c common
	c.register(fs)
	spec := fs.String("schema", "", `column layout, e.g. "id int4, name text, price numeric(10,2)"`)
	dataDir := fs.String("datadir", "", "PostgreSQL data directory (resolves schema, files, TOAST and pg_xact from the catalogs)")
	dbArg := fs.String("db", "", "database name or OID (with --datadir)")
	table := fs.String("table", "", "table name, optionally schema-qualified (with --datadir); dropped tables are found too")
	toastPath := fs.String("toast", "", "TOAST relation file, to rebuild out-of-line values")
	xactDir := fs.String("xact", "", "pg_xact directory (default: <datadir>/pg_xact)")
	states := fs.String("state", "all", "comma-separated states to output: live, deleted, updated, aborted, superseded, in-progress, deleting, unknown, all")
	remnants := fs.Bool("remnants", false, "also carve tuples out of page free space (rows removed by pruning/VACUUM)")
	format := fs.String("format", "jsonl", "output format: jsonl or csv")
	out := fs.String("out", "", "write rows here (never overwritten) plus <out>.manifest.json; default stdout")
	checksums := fs.String("checksums", "auto", "page checksum verification: auto, on, off (use off for carved pages)")
	pageSize := fs.Int("page-size", 8192, "page size (BLCKSZ)")
	pos, ok := a.parse(fs, &c, args, "[relation-file]")
	if !ok {
		return ExitUsage
	}
	started := time.Now().UTC()
	xl, err := loadXact(*dataDir, *xactDir)
	if err != nil {
		a.log.Warn("commit log unavailable; tuples without hint bits will be 'unknown'", "err", err)
		xl = nil
	}

	var cols []schema.Column
	relPath := ""
	if len(pos) == 1 {
		relPath = pos[0]
	} else if len(pos) > 1 {
		return a.usageErr(fs, "one relation file at most")
	}
	switch {
	case *table != "":
		if *dataDir == "" {
			return a.usageErr(fs, "--table needs --datadir")
		}
		db, err := openCatalog(*dataDir, *dbArg, xl)
		if err != nil {
			return a.fail(err)
		}
		rel, err := db.Find(*table)
		if err != nil {
			return a.fail(err)
		}
		cols = rel.Columns
		if len(cols) == 0 {
			return a.fail(fmt.Errorf("no column definitions found for %s", *table))
		}
		a.log.Info("resolved from catalog", "table", rel.Namespace+"."+rel.Name, "oid", rel.OID, "state", rel.State, "file", rel.Path, "schema", schema.String(cols))
		if relPath == "" {
			relPath = rel.Path
		}
		if *toastPath == "" && rel.ToastPath != "" {
			*toastPath = rel.ToastPath
		}
		if _, err := os.Stat(relPath); err != nil && rel.State != heap.Live {
			return a.fail(fmt.Errorf("%s was dropped and its file %s is gone; carve its pages from the disk (`pagerelic carve`) and pass the carved file as the argument; schema: %q", *table, rel.Path, schema.String(cols)))
		}
	case *spec != "":
		if cols, err = schema.Parse(*spec); err != nil {
			return a.usageErr(fs, "%v", err)
		}
		if relPath == "" {
			return a.usageErr(fs, "a relation file is required with --schema")
		}
	default:
		return a.usageErr(fs, "give --schema with a relation file, or --datadir --db --table")
	}

	want := map[heap.State]bool{}
	if *states != "all" {
		for _, s := range strings.Split(*states, ",") {
			want[heap.State(strings.TrimSpace(s))] = true
		}
	}
	var mode recover.ChecksumMode
	switch *checksums {
	case "auto":
		mode = recover.ChecksumAuto
	case "on":
		mode = recover.ChecksumOn
	case "off":
		mode = recover.ChecksumOff
	default:
		return a.usageErr(fs, "--checksums must be auto, on or off")
	}

	rel, err := relation.Open(relPath, *pageSize)
	if err != nil {
		return a.fail(err)
	}
	defer rel.Close()
	inputs := []string{relPath}
	var ts *toast.Store
	if *toastPath != "" {
		tr, err := relation.Open(*toastPath, *pageSize)
		if err != nil {
			return a.fail(fmt.Errorf("open TOAST relation: %w", err))
		}
		ts, err = toast.Load(tr)
		tr.Close()
		if err != nil {
			return a.fail(err)
		}
		inputs = append(inputs, *toastPath)
		a.log.Info("TOAST relation indexed", "chunks", ts.Chunks)
	}

	w := a.stdout
	var outFile *os.File
	if *out != "" {
		if outFile, err = createExclusive(*out); err != nil {
			return a.fail(err)
		}
		w = outFile
	}
	rw, err := newRowWriter(*format, w, cols)
	if err != nil {
		return a.usageErr(fs, "%v", err)
	}
	sum, scanErr := recover.Scan(a.ctx, rel, recover.Options{Columns: cols, Xact: xl, Toast: ts, Checksums: mode, Remnants: *remnants},
		func(p recover.PageReport) {
			for _, prob := range p.Problems {
				a.log.Warn("page problem", "block", p.Block, "problem", prob)
			}
		},
		func(r recover.Row) error {
			if len(want) > 0 && !want[r.State] {
				return nil
			}
			return rw.write(r)
		})
	if err := rw.close(); err != nil && scanErr == nil {
		scanErr = err
	}
	if outFile != nil {
		outFile.Sync()
		outFile.Close()
	}
	a.log.Info("scan complete", "pages", sum.Pages, "rows", sum.Rows, "remnants", sum.Remnants, "states", fmt.Sprint(sum.ByState),
		"checksum_failures", sum.ChecksumFailures, "invalid_pages", sum.InvalidPages, "rows_with_decode_errors", sum.DecodeErrors)
	if scanErr != nil {
		return a.fail(scanErr)
	}
	if *out != "" {
		m := manifest{Tool: buildinfo.Get(), Command: a.args, Started: started, Finished: time.Now().UTC(), Columns: cols, Summary: sum,
			Output: fileDigest{Path: *out, Size: rw.h.n, SHA256: rw.digest()}}
		for _, p := range inputs {
			if d, err := digestFile(p); err == nil {
				m.Inputs = append(m.Inputs, d)
			}
		}
		if err := writeManifest(*out+".manifest.json", m); err != nil {
			return a.fail(err)
		}
	}
	if sum.ChecksumFailures > 0 || sum.InvalidPages > 0 || sum.DecodeErrors > 0 {
		return ExitPartial
	}
	return ExitOK
}
