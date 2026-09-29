// Package heap decodes heap tuple headers (htup_details.h) and classifies
// each tuple version's MVCC state from its hint bits and, when available,
// the commit log.
package heap

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// HeaderSize is SizeofHeapTupleHeader (offsetof(t_bits)).
const HeaderSize = 23

// t_infomask bits.
const (
	HasNull        = 0x0001
	HasVarWidth    = 0x0002
	HasExternal    = 0x0004
	HasOIDOld      = 0x0008
	XmaxKeyShrLock = 0x0010
	ComboCID       = 0x0020
	XmaxExclLock   = 0x0040
	XmaxLockOnly   = 0x0080
	XminCommitted  = 0x0100
	XminInvalid    = 0x0200
	XmaxCommitted  = 0x0400
	XmaxInvalid    = 0x0800
	XmaxIsMulti    = 0x1000
	HeapUpdated    = 0x2000
	MovedOff       = 0x4000
	MovedIn        = 0x8000
	xminFrozen     = XminCommitted | XminInvalid
	lockMask       = XmaxKeyShrLock | XmaxExclLock
	natsMask       = 0x07FF
	KeysUpdated    = 0x2000 // t_infomask2
	HotUpdated     = 0x4000
	HeapOnlyTuple  = 0x8000
	FrozenXID      = 2
	BootstrapXID   = 1
	firstNormalXID = 3
)

// Header is HeapTupleHeaderData.
type Header struct {
	Xmin, Xmax, Field3 uint32
	CtidBlock          uint32
	CtidOffset         uint16
	Infomask2          uint16
	Infomask           uint16
	Hoff               uint8
	Bits               []byte // null bitmap (nil when HEAP_HASNULL is clear)
}

// Natts is the number of attributes stored in the tuple.
func (h Header) Natts() int { return int(h.Infomask2 & natsMask) }

// HasNulls reports HEAP_HASNULL.
func (h Header) HasNulls() bool { return h.Infomask&HasNull != 0 }

// IsNull reports whether attribute i (0-based) is NULL per the bitmap.
func (h Header) IsNull(i int) bool {
	if !h.HasNulls() {
		return false
	}
	if i/8 >= len(h.Bits) {
		return true
	}
	return h.Bits[i/8]&(1<<(i%8)) == 0
}

// Ctid renders t_ctid as PostgreSQL does.
func (h Header) Ctid() string { return fmt.Sprintf("(%d,%d)", h.CtidBlock, h.CtidOffset) }

// BitString renders t_bits as pageinspect's heap_page_items does.
func (h Header) BitString() string {
	if !h.HasNulls() {
		return ""
	}
	var b strings.Builder
	for i := 0; i < h.Natts(); i++ {
		if h.IsNull(i) {
			b.WriteByte('0')
		} else {
			b.WriteByte('1')
		}
	}
	// pageinspect prints whole bytes of the bitmap.
	for i := h.Natts(); i < ((h.Natts()+7)/8)*8; i++ {
		if i/8 < len(h.Bits) && h.Bits[i/8]&(1<<(i%8)) != 0 {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}

// ParseHeader decodes a tuple header from b, which starts at the tuple.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, fmt.Errorf("heap: tuple of %d bytes is shorter than its header", len(b))
	}
	le := binary.LittleEndian
	h := Header{
		Xmin:       le.Uint32(b[0:]),
		Xmax:       le.Uint32(b[4:]),
		Field3:     le.Uint32(b[8:]),
		CtidBlock:  uint32(le.Uint16(b[12:]))<<16 | uint32(le.Uint16(b[14:])),
		CtidOffset: le.Uint16(b[16:]),
		Infomask2:  le.Uint16(b[18:]),
		Infomask:   le.Uint16(b[20:]),
		Hoff:       b[22],
	}
	if int(h.Hoff) < HeaderSize || int(h.Hoff) > len(b) {
		return h, fmt.Errorf("heap: t_hoff %d outside tuple of %d bytes", h.Hoff, len(b))
	}
	if h.Hoff%8 != 0 {
		return h, fmt.Errorf("heap: t_hoff %d is not MAXALIGNed", h.Hoff)
	}
	if h.HasNulls() {
		n := (h.Natts() + 7) / 8
		if HeaderSize+n > int(h.Hoff) {
			return h, fmt.Errorf("heap: null bitmap for %d attributes overruns t_hoff %d", h.Natts(), h.Hoff)
		}
		h.Bits = b[HeaderSize : HeaderSize+n]
	} else if int(h.Hoff) != 24 && h.Infomask&HasOIDOld == 0 {
		return h, fmt.Errorf("heap: t_hoff %d without a null bitmap or OID", h.Hoff)
	}
	return h, nil
}

// FlagNames renders infomask and infomask2 bits.
func (h Header) FlagNames() []string {
	names := []struct {
		bit  uint16
		name string
	}{
		{HasNull, "HASNULL"}, {HasVarWidth, "HASVARWIDTH"}, {HasExternal, "HASEXTERNAL"}, {HasOIDOld, "HASOID_OLD"},
		{XmaxKeyShrLock, "XMAX_KEYSHR_LOCK"}, {ComboCID, "COMBOCID"}, {XmaxExclLock, "XMAX_EXCL_LOCK"},
		{XmaxLockOnly, "XMAX_LOCK_ONLY"}, {XminCommitted, "XMIN_COMMITTED"}, {XminInvalid, "XMIN_INVALID"},
		{XmaxCommitted, "XMAX_COMMITTED"}, {XmaxInvalid, "XMAX_INVALID"}, {XmaxIsMulti, "XMAX_IS_MULTI"},
		{HeapUpdated, "UPDATED"}, {MovedOff, "MOVED_OFF"}, {MovedIn, "MOVED_IN"},
	}
	var out []string
	for _, n := range names {
		if h.Infomask&n.bit != 0 {
			out = append(out, n.name)
		}
	}
	if h.Infomask2&KeysUpdated != 0 {
		out = append(out, "KEYS_UPDATED")
	}
	if h.Infomask2&HotUpdated != 0 {
		out = append(out, "HOT_UPDATED")
	}
	if h.Infomask2&HeapOnlyTuple != 0 {
		out = append(out, "HEAP_ONLY")
	}
	return out
}

// XmaxLockedOnly is HEAP_XMAX_IS_LOCKED_ONLY: xmax only locked the row.
func (h Header) XmaxLockedOnly() bool {
	return h.Infomask&XmaxLockOnly != 0 || h.Infomask&(XmaxIsMulti|lockMask) == XmaxExclLock
}
