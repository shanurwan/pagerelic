// Package recover extracts rows from heap relation pages: through line
// pointers (every tuple version still referenced, whatever its MVCC state)
// and by carving tuple remnants out of page free space, where rows removed
// by pruning or VACUUM can survive until the space is reused.
package recover

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/shanurwan/pagerelic/internal/datum"
	"github.com/shanurwan/pagerelic/internal/heap"
	"github.com/shanurwan/pagerelic/internal/schema"
	"github.com/shanurwan/pagerelic/internal/toast"
)

// Value is one decoded column.
type Value struct {
	Null    bool   `json:"null,omitempty"`
	Missing bool   `json:"missing,omitempty"` // column added after this tuple was written
	Text    string `json:"text,omitempty"`
	Err     string `json:"error,omitempty"`
}

// errStructural means the tuple's bytes are inconsistent with the schema.
var errStructural = errors.New("tuple does not match the column layout")

// DecodeValues decodes the attributes of tup (starting at the tuple header)
// following heap_deform_tuple's alignment rules. It returns the values for
// every non-dropped column and the offset just past the last attribute.
// A structural error stops decoding: later offsets would be meaningless.
func DecodeValues(tup []byte, h heap.Header, cols []schema.Column, ts *toast.Store) ([]Value, int, error) {
	off := int(h.Hoff)
	out := make([]Value, 0, len(cols))
	natts := h.Natts()
	if natts > len(cols) {
		return nil, off, fmt.Errorf("%w: tuple has %d attributes, schema has %d", errStructural, natts, len(cols))
	}
	for i, c := range cols {
		emit := func(v Value) {
			if !c.Dropped {
				out = append(out, v)
			}
		}
		if i >= natts {
			emit(Value{Null: true, Missing: true})
			continue
		}
		if h.IsNull(i) {
			emit(Value{Null: true})
			continue
		}
		var raw []byte
		switch {
		case c.Len > 0:
			off = datum.AlignTo(off, c.Align)
			end := off + int(c.Len)
			if end > len(tup) {
				return out, off, fmt.Errorf("%w: column %s runs past the tuple", errStructural, c.Name)
			}
			raw = tup[off:end]
			off = end
			if c.Dropped {
				continue
			}
			s, err := datum.Format(c.TypeOID, raw)
			emit(valueOf(s, err))
		case c.Len == -1:
			if off >= len(tup) {
				return out, off, fmt.Errorf("%w: column %s starts past the tuple", errStructural, c.Name)
			}
			if tup[off] == 0 { // pad byte: a 4-byte header follows at the aligned offset
				off = datum.AlignTo(off, c.Align)
			}
			if off >= len(tup) {
				return out, off, fmt.Errorf("%w: column %s starts past the tuple", errStructural, c.Name)
			}
			v, err := datum.ParseVarlena(tup[off:])
			if err != nil {
				return out, off, fmt.Errorf("%w: column %s: %v", errStructural, c.Name, err)
			}
			off += v.Size
			if c.Dropped {
				continue
			}
			emit(varlenaValue(c, v, ts))
		case c.Len == -2:
			end := bytes.IndexByte(tup[off:], 0)
			if end < 0 {
				return out, off, fmt.Errorf("%w: unterminated cstring in %s", errStructural, c.Name)
			}
			if !c.Dropped {
				emit(Value{Text: string(tup[off : off+end])})
			}
			off += end + 1
		default:
			return out, off, fmt.Errorf("%w: column %s has invalid attlen %d", errStructural, c.Name, c.Len)
		}
	}
	return out, off, nil
}

func valueOf(s string, err error) Value {
	if err != nil && !errors.Is(err, datum.ErrUnsupported) {
		return Value{Err: err.Error()}
	}
	v := Value{Text: s}
	if err != nil {
		v.Err = "type not decoded; shown as hex"
	}
	return v
}

func varlenaValue(c schema.Column, v datum.Varlena, ts *toast.Store) Value {
	switch v.Kind {
	case datum.VarPlain:
		return valueOf(datum.Format(c.TypeOID, v.Data))
	case datum.VarCompressed:
		raw, err := datum.Decompress(v.Method, v.Data, v.RawSize)
		if err != nil {
			return Value{Err: "inline compressed value: " + err.Error()}
		}
		return valueOf(datum.Format(c.TypeOID, raw))
	default:
		p := v.Toast
		if ts == nil {
			return Value{Err: fmt.Sprintf("TOASTed value (%d bytes, chunk_id %d in TOAST relation %d); pass the TOAST relation to recover it", p.RawSize-4, p.ValueID, p.ToastRelID)}
		}
		raw, err := ts.Fetch(p)
		if err != nil {
			return Value{Err: err.Error()}
		}
		return valueOf(datum.Format(c.TypeOID, raw))
	}
}

// decodeErrors counts values that failed to decode.
func decodeErrors(vals []Value) int {
	n := 0
	for _, v := range vals {
		if v.Err != "" && v.Err != "type not decoded; shown as hex" {
			n++
		}
	}
	return n
}
