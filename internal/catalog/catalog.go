package catalog

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/shanurwan/pagerelic/internal/datum"
	"github.com/shanurwan/pagerelic/internal/heap"
	"github.com/shanurwan/pagerelic/internal/page"
	"github.com/shanurwan/pagerelic/internal/recover"
	"github.com/shanurwan/pagerelic/internal/relation"
	"github.com/shanurwan/pagerelic/internal/schema"
)

// Tuple is one decoded catalog row version.
type Tuple struct {
	State  heap.State
	Xmin   uint32
	Fields map[string]recover.Value
}

func (t Tuple) str(name string) string { return t.Fields[name].Text }

func (t Tuple) num(name string) int64 {
	n, _ := strconv.ParseInt(t.Fields[name].Text, 10, 64)
	return n
}

// Attr is a pg_attribute row version.
type Attr struct {
	RelID  uint32
	Num    int16
	Column schema.Column
	State  heap.State
	Xmin   uint32
}

// Relation is a pg_class entry with everything needed to read its data.
type Relation struct {
	OID       uint32          `json:"oid"`
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	Kind      string          `json:"kind"`
	FileNode  uint32          `json:"filenode"`
	Path      string          `json:"path"`
	ToastOID  uint32          `json:"toast_oid,omitempty"`
	ToastPath string          `json:"toast_path,omitempty"`
	State     heap.State      `json:"state"` // live, or deleted for dropped relations
	Columns   []schema.Column `json:"columns"`
}

// DB is one database directory (base/<oid>).
type DB struct {
	Dir      string
	PageSize int
	Map      FileNodeMap
	Xact     heap.XactLookup
	attrs    []Attr
	classes  []Tuple
	nsNames  map[uint32]string
}

// Open bootstraps the catalogs of a database directory.
func Open(dbDir string, xl heap.XactLookup) (*DB, error) {
	m, err := ReadFileNodeMap(filepath.Join(dbDir, "pg_filenode.map"))
	if err != nil {
		return nil, err
	}
	db := &DB{Dir: dbDir, PageSize: page.DefaultSize, Map: m, Xact: xl}
	if err := db.loadAttributes(); err != nil {
		return nil, err
	}
	classCols, err := db.columnsFor(OIDPgClass, heap.Live)
	if err != nil {
		return nil, fmt.Errorf("catalog: pg_class layout: %w", err)
	}
	if db.classes, err = db.decode(db.mappedPath(OIDPgClass), classCols); err != nil {
		return nil, err
	}
	db.loadNamespaces()
	return db, nil
}

func (db *DB) mappedPath(oid uint32) string {
	if fn, ok := db.Map[oid]; ok {
		return filepath.Join(db.Dir, strconv.FormatUint(uint64(fn), 10))
	}
	return filepath.Join(db.Dir, strconv.FormatUint(uint64(oid), 10))
}

// decode reads every tuple version (through line pointers) of a catalog.
func (db *DB) decode(path string, cols []schema.Column) ([]Tuple, error) {
	rel, err := relation.Open(path, db.PageSize)
	if err != nil {
		return nil, err
	}
	defer rel.Close()
	vis := schema.Visible(cols)
	var out []Tuple
	_, err = recover.Scan(context.Background(), rel, recover.Options{Columns: cols, Xact: db.Xact}, nil, func(r recover.Row) error {
		t := Tuple{State: r.State, Xmin: r.Xmin, Fields: map[string]recover.Value{}}
		for i, c := range vis {
			t.Fields[c.Name] = r.Values[i]
		}
		out = append(out, t)
		return nil
	})
	return out, err
}

// pg_attribute's first three columns are the same in every supported
// version: attrelid oid, attname name, atttypid oid.
const (
	offAttrelid = 0
	offAttname  = 4
	offAtttypid = 68
)

// systemAttributes have negative attnum and are not stored in tuples.
var systemAttributes = map[string]bool{"ctid": true, "xmin": true, "cmin": true, "xmax": true, "cmax": true, "tableoid": true, "oid": false}

// loadAttributes derives pg_attribute's own layout from the rows that
// describe pg_attribute itself, verifies it, then decodes the catalog.
func (db *DB) loadAttributes() error {
	path := db.mappedPath(OIDPgAttribute)
	rel, err := relation.Open(path, db.PageSize)
	if err != nil {
		return err
	}
	type selfRow struct {
		name string
		typ  uint32
	}
	var self []selfRow
	buf := make([]byte, db.PageSize)
	for blk := uint32(0); blk < rel.Blocks(); blk++ {
		if err := rel.ReadPage(blk, buf); err != nil {
			rel.Close()
			return err
		}
		h, err := page.ParseHeader(buf)
		if err != nil || len(h.Problems(db.PageSize)) > 0 {
			continue
		}
		for _, it := range page.Items(buf, h) {
			if it.Flags != page.LPNormal || page.ItemProblem(it, h, db.PageSize) != "" {
				continue
			}
			tup := buf[it.Offset : it.Offset+it.Length]
			th, err := heap.ParseHeader(tup)
			if err != nil || int(th.Hoff)+offAtttypid+4 > len(tup) {
				continue
			}
			if heap.Classify(th, blk, uint16(it.Index), db.Xact).State != heap.Live {
				continue
			}
			d := tup[th.Hoff:]
			if binary.LittleEndian.Uint32(d[offAttrelid:]) != OIDPgAttribute {
				continue
			}
			name, _ := datum.Format(datum.OIDName, d[offAttname:offAttname+64])
			if systemAttributes[name] {
				continue // attnum < 0: not part of the tuple layout
			}
			self = append(self, selfRow{name: name, typ: binary.LittleEndian.Uint32(d[offAtttypid:])})
		}
	}
	rel.Close()
	if len(self) < 10 {
		return fmt.Errorf("catalog: found only %d self-describing pg_attribute rows; catalog damaged or not a PostgreSQL 12+ database", len(self))
	}
	var cols []schema.Column
	for _, s := range self {
		t, ok := datum.Lookup(s.typ)
		if !ok {
			return fmt.Errorf("catalog: pg_attribute column %s has unknown type %d", s.name, s.typ)
		}
		cols = append(cols, schema.Column{Name: s.name, TypeOID: t.OID, TypeName: t.Name, Len: t.Len, Align: t.Align, ByVal: t.ByVal})
	}
	for _, need := range []string{"attrelid", "attname", "atttypid", "attlen", "attnum", "attalign", "attbyval", "attisdropped"} {
		if !hasColumn(cols, need) {
			return fmt.Errorf("catalog: derived pg_attribute layout lacks %s", need)
		}
	}
	tuples, err := db.decode(path, cols)
	if err != nil {
		return err
	}
	// Verify: pg_attribute's own rows must describe exactly the layout used
	// to read them (attnum 1..n in physical order, same attlen/attalign).
	pos := 0
	for _, t := range tuples {
		if t.State != heap.Live || t.num("attrelid") != OIDPgAttribute || t.num("attnum") <= 0 {
			continue
		}
		c := cols[pos]
		if t.num("attnum") != int64(pos+1) || t.str("attname") != c.Name ||
			t.num("attlen") != int64(c.Len) || t.str("attalign") != string(c.Align) {
			return fmt.Errorf("catalog: self-check failed at pg_attribute column %d (%s): layout could not be bootstrapped", pos+1, c.Name)
		}
		pos++
	}
	if pos != len(cols) {
		return fmt.Errorf("catalog: self-check matched %d of %d pg_attribute columns", pos, len(cols))
	}
	for _, t := range tuples {
		a := Attr{RelID: uint32(t.num("attrelid")), Num: int16(t.num("attnum")), State: t.State, Xmin: t.Xmin}
		align := t.str("attalign")
		if align == "" {
			continue
		}
		a.Column = schema.Column{
			Name: t.str("attname"), TypeOID: uint32(t.num("atttypid")), Len: int16(t.num("attlen")),
			Align: align[0], ByVal: t.str("attbyval") == "t", TypMod: int32(t.num("atttypmod")),
			Dropped: t.str("attisdropped") == "t",
		}
		if bt, ok := datum.Lookup(a.Column.TypeOID); ok {
			a.Column.TypeName = bt.Name
			if et, isArr := datum.Lookup(bt.Elem); isArr && strings.HasPrefix(bt.Name, "_") {
				a.Column.TypeName = et.Name + "[]"
			}
		} else {
			a.Column.TypeName = "oid:" + strconv.FormatUint(uint64(a.Column.TypeOID), 10)
		}
		db.attrs = append(db.attrs, a)
	}
	return nil
}

func hasColumn(cols []schema.Column, name string) bool {
	for _, c := range cols {
		if c.Name == name {
			return true
		}
	}
	return false
}

// columnsFor returns a relation's user columns (attnum > 0, including
// dropped ones, which still occupy space in old tuples). For state Live it
// uses live rows; otherwise the most recent version of each attnum.
func (db *DB) columnsFor(relID uint32, state heap.State) ([]schema.Column, error) {
	best := map[int16]Attr{}
	for _, a := range db.attrs {
		if a.RelID != relID || a.Num <= 0 {
			continue
		}
		if state == heap.Live && a.State != heap.Live {
			continue
		}
		cur, ok := best[a.Num]
		if !ok || (cur.State != heap.Live && a.State == heap.Live) || (cur.State == a.State && a.Xmin > cur.Xmin) {
			best[a.Num] = a
		}
	}
	if len(best) == 0 {
		return nil, fmt.Errorf("no pg_attribute rows for relation %d", relID)
	}
	nums := make([]int, 0, len(best))
	for n := range best {
		nums = append(nums, int(n))
	}
	sort.Ints(nums)
	var cols []schema.Column
	for i, n := range nums {
		if n != i+1 {
			return nil, fmt.Errorf("pg_attribute rows for relation %d skip attnum %d", relID, i+1)
		}
		cols = append(cols, best[int16(n)].Column)
	}
	return cols, nil
}

func (db *DB) loadNamespaces() {
	db.nsNames = map[uint32]string{}
	cols, err := db.columnsFor(OIDPgNamespace, heap.Live)
	if err != nil {
		return
	}
	fn := uint32(OIDPgNamespace)
	for _, c := range db.classes {
		if c.num("oid") == OIDPgNamespace && c.State == heap.Live {
			if v := uint32(c.num("relfilenode")); v != 0 {
				fn = v
			}
		}
	}
	rows, err := db.decode(filepath.Join(db.Dir, strconv.FormatUint(uint64(fn), 10)), cols)
	if err != nil {
		return
	}
	for _, r := range rows {
		if r.State == heap.Live {
			db.nsNames[uint32(r.num("oid"))] = r.str("nspname")
		}
	}
}

// Relations lists tables, materialized views and TOAST tables. With
// dropped=true, relations whose pg_class row was deleted are included.
func (db *DB) Relations(dropped bool) []Relation {
	var out []Relation
	seen := map[uint32]bool{}
	for _, c := range db.classes {
		kind := c.str("relkind")
		if kind != "r" && kind != "m" && kind != "t" && kind != "p" {
			continue
		}
		live := c.State == heap.Live
		if !live && (!dropped || c.State != heap.Deleted) {
			continue
		}
		oid := uint32(c.num("oid"))
		if seen[oid] {
			continue
		}
		seen[oid] = true
		r := Relation{OID: oid, Name: c.str("relname"), Kind: kind, FileNode: uint32(c.num("relfilenode")),
			ToastOID: uint32(c.num("reltoastrelid")), State: c.State, Namespace: db.nsNames[uint32(c.num("relnamespace"))]}
		if r.FileNode == 0 {
			r.FileNode = db.Map[oid]
		}
		if r.FileNode != 0 {
			r.Path = filepath.Join(db.Dir, strconv.FormatUint(uint64(r.FileNode), 10))
		}
		cols, err := db.columnsFor(oid, map[bool]heap.State{true: heap.Live, false: heap.Deleted}[live])
		if err == nil {
			r.Columns = cols
		}
		out = append(out, r)
	}
	for i := range out {
		for _, t := range out {
			if t.OID == out[i].ToastOID && out[i].ToastOID != 0 {
				out[i].ToastPath = t.Path
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OID < out[j].OID })
	return out
}

// ErrNotFound is returned by Find.
var ErrNotFound = errors.New("catalog: relation not found")

// Find resolves "name" or "schema.name" among live relations, falling back
// to dropped ones.
func (db *DB) Find(qualified string) (Relation, error) {
	ns, name, ok := strings.Cut(qualified, ".")
	if !ok {
		ns, name = "", qualified
	}
	for _, withDropped := range []bool{false, true} {
		var hits []Relation
		for _, r := range db.Relations(withDropped) {
			if r.Name == name && (ns == "" || r.Namespace == ns) {
				hits = append(hits, r)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil
		default:
			return hits[len(hits)-1], nil // most recent OID
		}
	}
	return Relation{}, fmt.Errorf("%w: %s", ErrNotFound, qualified)
}
