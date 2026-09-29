package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/shanurwan/pagerelic/internal/heap"
)

// Database is a pg_database entry.
type Database struct {
	OID  uint32 `json:"oid"`
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

// Databases lists the databases of a data directory by decoding the shared
// pg_database catalog (global/). Its layout comes from the pg_attribute of
// any readable database, since every database describes the shared catalogs.
func Databases(dataDir string, xl heap.XactLookup) ([]Database, error) {
	gm, err := ReadFileNodeMap(filepath.Join(dataDir, "global", "pg_filenode.map"))
	if err != nil {
		return nil, err
	}
	fn, ok := gm[OIDPgDatabase]
	if !ok {
		return nil, fmt.Errorf("catalog: pg_database missing from global/pg_filenode.map")
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "base"))
	if err != nil {
		return nil, err
	}
	var db *DB
	var lastErr error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if db, lastErr = Open(filepath.Join(dataDir, "base", e.Name()), xl); lastErr == nil {
			break
		}
	}
	if db == nil {
		return nil, fmt.Errorf("catalog: no database directory could be bootstrapped: %w", lastErr)
	}
	cols, err := db.columnsFor(OIDPgDatabase, heap.Live)
	if err != nil {
		return nil, err
	}
	rows, err := db.decode(filepath.Join(dataDir, "global", strconv.FormatUint(uint64(fn), 10)), cols)
	if err != nil {
		return nil, err
	}
	var out []Database
	for _, r := range rows {
		if r.State != heap.Live {
			continue
		}
		oid := uint32(r.num("oid"))
		out = append(out, Database{OID: oid, Name: r.str("datname"), Dir: filepath.Join(dataDir, "base", strconv.FormatUint(uint64(oid), 10))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out, nil
}
