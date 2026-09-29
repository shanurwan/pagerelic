// Package relation reads a PostgreSQL relation fork as a sequence of pages,
// across its 1 GiB segment files (16384, 16384.1, 16384.2, ...). Files are
// opened read-only.
package relation

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// SegmentSize is RELSEG_SIZE × BLCKSZ for the default build (1 GiB).
const SegmentSize = 1 << 30

type segment struct {
	r    io.ReaderAt
	size int64
	name string
	c    io.Closer
}

// Relation is an ordered set of segment files.
type Relation struct {
	Path     string
	PageSize int
	segs     []segment
	// Trailing bytes that do not form a whole page (torn extension).
	Partial int64
}

// Open opens path and any following segment files.
func Open(path string, pageSize int) (*Relation, error) {
	r := &Relation{Path: path, PageSize: pageSize}
	for i := 0; ; i++ {
		p := path
		if i > 0 {
			p = path + "." + strconv.Itoa(i)
		}
		f, err := os.Open(p)
		if err != nil {
			if i > 0 && errors.Is(err, os.ErrNotExist) {
				break
			}
			r.Close()
			return nil, err
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			r.Close()
			return nil, err
		}
		r.segs = append(r.segs, segment{r: f, size: st.Size(), name: filepath.Base(p), c: f})
		if st.Size() < SegmentSize {
			break // only a full segment can be followed by another
		}
	}
	r.finish()
	return r, nil
}

// FromBytes wraps an in-memory relation image (one segment).
func FromBytes(name string, b []byte, pageSize int) *Relation {
	r := &Relation{Path: name, PageSize: pageSize, segs: []segment{{r: bytes.NewReader(b), size: int64(len(b)), name: name}}}
	r.finish()
	return r
}

func (r *Relation) finish() {
	if n := len(r.segs); n > 0 {
		r.Partial = r.segs[n-1].size % int64(r.PageSize)
	}
}

// Blocks returns the number of whole pages.
func (r *Relation) Blocks() uint32 {
	var n int64
	for _, s := range r.segs {
		n += s.size / int64(r.PageSize)
	}
	return uint32(n)
}

// ReadPage reads block blk into buf (len PageSize).
func (r *Relation) ReadPage(blk uint32, buf []byte) error {
	perSeg := uint32(SegmentSize / r.PageSize)
	seg, off := int(blk/perSeg), int64(blk%perSeg)*int64(r.PageSize)
	if seg >= len(r.segs) || off+int64(r.PageSize) > r.segs[seg].size {
		return fmt.Errorf("relation: block %d beyond end of %s", blk, r.Path)
	}
	_, err := r.segs[seg].r.ReadAt(buf[:r.PageSize], off)
	return err
}

// Close closes all segment files.
func (r *Relation) Close() error {
	var errs []error
	for _, s := range r.segs {
		if s.c != nil {
			errs = append(errs, s.c.Close())
		}
	}
	return errors.Join(errs...)
}
