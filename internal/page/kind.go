package page

import "encoding/binary"

// Kind is the access method a page belongs to, inferred from its special
// space (the only place a page records what it is).
type Kind string

const (
	KindHeap     Kind = "heap"
	KindBTree    Kind = "btree"
	KindHash     Kind = "hash"
	KindGiST     Kind = "gist"
	KindSPGiST   Kind = "spgist"
	KindGIN      Kind = "gin"
	KindBRIN     Kind = "brin"
	KindSequence Kind = "sequence"
	KindNew      Kind = "new"
	KindUnknown  Kind = "unknown"
)

const (
	hashPageID   = 0xFF80
	gistPageID   = 0xFF81
	spgistPageID = 0xFF82
	seqMagic     = 0x1717
)

// DetectKind classifies a page with a valid header.
func DetectKind(b []byte, h Header) Kind {
	if h.Upper == 0 {
		return KindNew
	}
	size := int(h.Special)
	end := min(len(b), h.Size)
	if h.Special == 0 || size > end {
		return KindUnknown // never trust the declared page size for slicing
	}
	special := b[size:end]
	n := len(special)
	switch {
	case n == 0:
		return KindHeap
	case n >= 2:
		id := binary.LittleEndian.Uint16(special[n-2:])
		switch id {
		case hashPageID:
			return KindHash
		case gistPageID:
			return KindGiST
		case spgistPageID:
			return KindSPGiST
		}
		if n == 8 && binary.LittleEndian.Uint32(special) == seqMagic {
			return KindSequence
		}
		if n == 8 && id >= 0xF091 && id <= 0xF093 {
			return KindBRIN
		}
		if n == 16 {
			// BTPageOpaqueData: prev, next, level (uint32), btpo_flags, cycle id.
			if flags := binary.LittleEndian.Uint16(special[12:]); flags < 0x0400 {
				return KindBTree
			}
		}
		if n == 8 {
			return KindGIN
		}
	}
	return KindUnknown
}
