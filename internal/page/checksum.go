package page

import "encoding/binary"

// PostgreSQL's data checksum (src/include/storage/checksum_impl.h): 32
// parallel FNV-1a-like lanes over the page viewed as uint32 words, two extra
// mixing rounds, XOR-folded, then mixed with the block number and reduced
// to 1..65535 so that zero can mean "no checksum".

const (
	nSums    = 32
	fnvPrime = 16777619
)

var checksumBaseOffsets = [nSums]uint32{
	0x5B1F36E9, 0xB8525960, 0x02AB50AA, 0x1DE66D2A,
	0x79FF467A, 0x9BB9F8A3, 0x217E7CD2, 0x83E13D2C,
	0xF8D4474F, 0xE39EB970, 0x42C6AE16, 0x993216FA,
	0x7B093B5D, 0x98DAFF3C, 0xF718902A, 0x0B1C9CDB,
	0xE58F764B, 0x187636BC, 0x5D7B3BB1, 0xE73DE7DE,
	0x92BEC979, 0xCCA6C0B2, 0x304A0979, 0x85AA43D4,
	0x783125BB, 0x6CA8EAA2, 0xE407EAC6, 0x4B5CFC3E,
	0x9FBF8C76, 0x15CA20BE, 0xF2CA9FD3, 0x959BD756,
}

func comp(sum, value uint32) uint32 {
	tmp := sum ^ value
	return tmp*fnvPrime ^ tmp>>17
}

// BlockSum returns pg_checksum_block for page p with pd_checksum treated as
// zero. It is independent of the block number, which lets a carver compute
// it once and then test candidate block numbers cheaply.
func BlockSum(p []byte) uint32 {
	sums := checksumBaseOffsets
	words := len(p) / 4
	for i := 0; i < words/nSums; i++ {
		for j := 0; j < nSums; j++ {
			w := i*nSums + j
			v := binary.LittleEndian.Uint32(p[4*w:])
			if w == 2 { // bytes 8..11: pd_checksum (low half) and pd_flags
				v &^= 0x0000FFFF
			}
			sums[j] = comp(sums[j], v)
		}
	}
	for r := 0; r < 2; r++ {
		for j := 0; j < nSums; j++ {
			sums[j] = comp(sums[j], 0)
		}
	}
	var result uint32
	for _, s := range sums {
		result ^= s
	}
	return result
}

// FoldChecksum mixes a BlockSum with an absolute block number (segment
// number × blocks-per-segment + block in segment) into the stored value.
func FoldChecksum(sum uint32, blkno uint32) uint16 {
	return uint16((sum^blkno)%65535 + 1)
}

// Checksum is pg_checksum_page.
func Checksum(p []byte, blkno uint32) uint16 { return FoldChecksum(BlockSum(p), blkno) }

// CandidateBlocks returns block numbers in [0, limit) for which a page with
// the given BlockSum would carry the stored checksum, up to max results.
// For pages carved off raw media this narrows down where they came from.
func CandidateBlocks(sum uint32, stored uint16, limit uint32, max int) []uint32 {
	var out []uint32
	for b := uint32(0); b < limit && len(out) < max; b++ {
		if FoldChecksum(sum, b) == stored {
			out = append(out, b)
		}
	}
	return out
}
