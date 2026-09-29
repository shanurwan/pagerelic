// Package page decodes PostgreSQL's generic page layout (bufpage.h): the
// 24-byte page header, the line pointer array, the special space, and the
// data-checksum algorithm (checksum_impl.h).
//
// Everything here works on untrusted bytes: nothing indexes a slice without
// checking bounds first, and malformed pages are reported, never panicked on.
package page

import (
	"encoding/binary"
	"fmt"
)

const (
	// HeaderSize is SizeOfPageHeaderData.
	HeaderSize = 24
	// LayoutVersion is PG_PAGE_LAYOUT_VERSION (PostgreSQL 8.3 and later).
	LayoutVersion = 4
	// DefaultSize is the default BLCKSZ.
	DefaultSize = 8192
	// MaxAlign is MAXIMUM_ALIGNOF on every supported 64-bit platform.
	MaxAlign = 8

	flagHasFreeLines = 0x0001
	flagPageFull     = 0x0002
	flagAllVisible   = 0x0004
	validFlagBits    = 0x0007
)

// Header is PageHeaderData.
type Header struct {
	LSN      LSN
	Checksum uint16
	Flags    uint16
	Lower    uint16
	Upper    uint16
	Special  uint16
	Size     int // page size encoded in pd_pagesize_version
	Version  uint8
	PruneXID uint32
}

// LSN is a WAL position.
type LSN uint64

func (l LSN) String() string { return fmt.Sprintf("%X/%X", uint32(l>>32), uint32(l)) }

// ParseHeader decodes the first 24 bytes of a page.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderSize {
		return Header{}, fmt.Errorf("page: %d bytes is shorter than a page header", len(b))
	}
	le := binary.LittleEndian
	sv := le.Uint16(b[18:])
	return Header{
		LSN:      LSN(uint64(le.Uint32(b[0:]))<<32 | uint64(le.Uint32(b[4:]))),
		Checksum: le.Uint16(b[8:]),
		Flags:    le.Uint16(b[10:]),
		Lower:    le.Uint16(b[12:]),
		Upper:    le.Uint16(b[14:]),
		Special:  le.Uint16(b[16:]),
		Size:     int(sv & 0xFF00),
		Version:  uint8(sv & 0x00FF),
		PruneXID: le.Uint32(b[20:]),
	}, nil
}

// FlagNames renders pd_flags.
func (h Header) FlagNames() []string {
	var out []string
	if h.Flags&flagHasFreeLines != 0 {
		out = append(out, "HAS_FREE_LINES")
	}
	if h.Flags&flagPageFull != 0 {
		out = append(out, "PAGE_FULL")
	}
	if h.Flags&flagAllVisible != 0 {
		out = append(out, "ALL_VISIBLE")
	}
	if h.Flags&^validFlagBits != 0 {
		out = append(out, fmt.Sprintf("INVALID(0x%04X)", h.Flags&^validFlagBits))
	}
	return out
}

// AllVisible reports PD_ALL_VISIBLE.
func (h Header) AllVisible() bool { return h.Flags&flagAllVisible != 0 }

// Problems lists every way the header violates PostgreSQL's own sanity
// checks (PageIsVerifiedExtended) for a page of pageSize bytes. An empty
// result means the header is structurally valid.
func (h Header) Problems(pageSize int) []string {
	var p []string
	if h.Flags&^validFlagBits != 0 {
		p = append(p, fmt.Sprintf("pd_flags has undefined bits 0x%04X", h.Flags&^validFlagBits))
	}
	if int(h.Lower) < HeaderSize {
		p = append(p, fmt.Sprintf("pd_lower %d is inside the page header", h.Lower))
	}
	if h.Lower > h.Upper {
		p = append(p, fmt.Sprintf("pd_lower %d > pd_upper %d", h.Lower, h.Upper))
	}
	if h.Upper > h.Special {
		p = append(p, fmt.Sprintf("pd_upper %d > pd_special %d", h.Upper, h.Special))
	}
	if int(h.Special) > pageSize {
		p = append(p, fmt.Sprintf("pd_special %d is beyond the %d-byte page", h.Special, pageSize))
	}
	if h.Special%MaxAlign != 0 {
		p = append(p, fmt.Sprintf("pd_special %d is not MAXALIGNed", h.Special))
	}
	if h.Size != pageSize {
		p = append(p, fmt.Sprintf("pd_pagesize_version declares %d-byte pages, expected %d", h.Size, pageSize))
	}
	if h.Version != LayoutVersion {
		p = append(p, fmt.Sprintf("page layout version %d, expected %d", h.Version, LayoutVersion))
	}
	return p
}

// IsNew reports PageIsNew: pd_upper == 0. PostgreSQL treats such pages as
// valid-but-uninitialised (typically all zeroes after relation extension).
func IsNew(b []byte) bool {
	return len(b) >= HeaderSize && binary.LittleEndian.Uint16(b[14:]) == 0
}

// IsZero reports whether every byte is zero.
func IsZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// Line pointer flags (itemid.h).
const (
	LPUnused   = 0
	LPNormal   = 1
	LPRedirect = 2
	LPDead     = 3
)

// ItemID is a line pointer.
type ItemID struct {
	Index  int // 1-based offset number
	Offset uint16
	Flags  uint8
	Length uint16
}

// FlagName renders the line pointer state.
func (i ItemID) FlagName() string {
	switch i.Flags {
	case LPUnused:
		return "unused"
	case LPNormal:
		return "normal"
	case LPRedirect:
		return "redirect"
	}
	return "dead"
}

// HasStorage reports whether the item points at tuple bytes.
func (i ItemID) HasStorage() bool { return i.Length != 0 }

// ParseItemID decodes one 4-byte ItemIdData (lp_off:15, lp_flags:2, lp_len:15).
func ParseItemID(b []byte, index int) ItemID {
	v := binary.LittleEndian.Uint32(b)
	return ItemID{Index: index, Offset: uint16(v & 0x7FFF), Flags: uint8(v >> 15 & 0x3), Length: uint16(v >> 17)}
}

// Items decodes the line pointer array between the header and pd_lower. It
// never reads past pd_lower or the page.
func Items(b []byte, h Header) []ItemID {
	end := min(int(h.Lower), len(b))
	var out []ItemID
	for off, i := HeaderSize, 1; off+4 <= end; off, i = off+4, i+1 {
		out = append(out, ParseItemID(b[off:], i))
	}
	return out
}

// ItemProblem checks one line pointer against the page's bounds.
func ItemProblem(it ItemID, h Header, pageSize int) string {
	switch it.Flags {
	case LPUnused:
		if it.Offset != 0 || it.Length != 0 {
			return fmt.Sprintf("unused line pointer %d has offset %d / length %d", it.Index, it.Offset, it.Length)
		}
	case LPRedirect:
		if it.Length != 0 {
			return fmt.Sprintf("redirect line pointer %d has length %d", it.Index, it.Length)
		}
	default:
		if !it.HasStorage() {
			if it.Flags == LPNormal {
				return fmt.Sprintf("normal line pointer %d has no storage", it.Index)
			}
			return ""
		}
		end := int(it.Offset) + int(it.Length)
		if int(it.Offset) < int(h.Upper) || end > int(h.Special) || end > pageSize {
			return fmt.Sprintf("line pointer %d [%d,%d) lies outside the tuple area [%d,%d)", it.Index, it.Offset, end, h.Upper, h.Special)
		}
		if it.Offset%MaxAlign != 0 {
			return fmt.Sprintf("line pointer %d offset %d is not MAXALIGNed", it.Index, it.Offset)
		}
	}
	return ""
}
