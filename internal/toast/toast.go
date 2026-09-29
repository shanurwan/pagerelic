// Package toast reassembles out-of-line (TOASTed) values from a TOAST
// relation. Chunks are indexed from every tuple on every page regardless
// of MVCC state: chunks of deleted rows remain until VACUUM, and they are
// exactly what a forensic recovery needs.
package toast

import (
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/shanurwan/pagerelic/internal/datum"
	"github.com/shanurwan/pagerelic/internal/heap"
	"github.com/shanurwan/pagerelic/internal/page"
	"github.com/shanurwan/pagerelic/internal/relation"
)

// Store maps chunk_id → chunk_seq → chunk bytes.
type Store struct {
	chunks map[uint32]map[int32][]byte
	Chunks int
	Bad    int // tuples that did not decode as TOAST chunks
}

// Load indexes every chunk tuple of a TOAST relation. Pages with invalid
// headers are skipped (their chunks become "missing" at fetch time).
func Load(rel *relation.Relation) (*Store, error) {
	s := &Store{chunks: map[uint32]map[int32][]byte{}}
	buf := make([]byte, rel.PageSize)
	for blk := uint32(0); blk < rel.Blocks(); blk++ {
		if err := rel.ReadPage(blk, buf); err != nil {
			return nil, err
		}
		h, err := page.ParseHeader(buf)
		if err != nil || len(h.Problems(rel.PageSize)) > 0 {
			continue
		}
		for _, it := range page.Items(buf, h) {
			if it.Flags != page.LPNormal || page.ItemProblem(it, h, rel.PageSize) != "" {
				continue
			}
			if !s.add(buf[it.Offset : it.Offset+it.Length]) {
				s.Bad++
			}
		}
	}
	return s, nil
}

// add decodes (chunk_id oid, chunk_seq int4, chunk_data bytea).
func (s *Store) add(tup []byte) bool {
	h, err := heap.ParseHeader(tup)
	if err != nil || h.Natts() != 3 || h.HasNulls() {
		return false
	}
	off := int(h.Hoff)
	if off+8 > len(tup) {
		return false
	}
	id := binary.LittleEndian.Uint32(tup[off:])
	seq := int32(binary.LittleEndian.Uint32(tup[off+4:]))
	off += 8
	if tup[off] == 0 { // 4-byte header: aligned to 'i'
		off = datum.AlignTo(off, 'i')
	}
	v, err := datum.ParseVarlena(tup[off:])
	if err != nil || v.Kind != datum.VarPlain {
		return false
	}
	m := s.chunks[id]
	if m == nil {
		m = map[int32][]byte{}
		s.chunks[id] = m
	}
	m[seq] = append([]byte(nil), v.Data...)
	s.Chunks++
	return true
}

// Fetch reassembles and, if needed, decompresses a TOASTed value.
func (s *Store) Fetch(p datum.ToastPointer) ([]byte, error) {
	m := s.chunks[p.ValueID]
	if len(m) == 0 {
		return nil, fmt.Errorf("toast: value %d not found in the TOAST relation (chunks vacuumed or wrong relation)", p.ValueID)
	}
	seqs := make([]int, 0, len(m))
	for q := range m {
		seqs = append(seqs, int(q))
	}
	sort.Ints(seqs)
	var data []byte
	for i, q := range seqs {
		if q != i {
			return nil, fmt.Errorf("toast: value %d is missing chunk %d (have %d chunks)", p.ValueID, i, len(seqs))
		}
		data = append(data, m[int32(q)]...)
	}
	if int32(len(data)) != p.ExtSize {
		return nil, fmt.Errorf("toast: value %d reassembled to %d bytes, pointer says %d", p.ValueID, len(data), p.ExtSize)
	}
	if !p.Compressed() {
		return data, nil
	}
	if len(data) < 4 {
		return nil, fmt.Errorf("toast: compressed value %d too short", p.ValueID)
	}
	method := int(binary.LittleEndian.Uint32(data) >> 30)
	return datum.Decompress(method, data[4:], int(p.RawSize)-4)
}
