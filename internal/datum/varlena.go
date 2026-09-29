// Package datum decodes PostgreSQL on-disk values: varlena headers, TOAST
// pointers, pglz/LZ4 compression, and text output for built-in types that
// matches PostgreSQL's own output functions.
package datum

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Compression methods (toast_compression.h).
const (
	CompressPGLZ = 0
	CompressLZ4  = 1
)

// VarKind classifies a varlena.
type VarKind int

const (
	VarPlain      VarKind = iota // 1-byte or 4-byte header, inline, uncompressed
	VarCompressed                // 4-byte header, inline, compressed
	VarExternal                  // TOAST pointer
)

// Varlena is a decoded varlena header.
type Varlena struct {
	Kind VarKind
	Size int    // total bytes occupied in the tuple, header included
	Data []byte // payload (plain), or compressed payload (compressed)
	// Compressed inline values:
	RawSize int
	Method  int
	// External values:
	Toast ToastPointer
}

// ToastPointer is varatt_external.
type ToastPointer struct {
	RawSize    int32  // va_rawsize: original size including its 4-byte header
	ExtSize    int32  // stored (possibly compressed) size without header
	Method     int    // compression method when compressed
	ValueID    uint32 // chunk_id in the TOAST relation
	ToastRelID uint32 // pg_class OID of the TOAST relation
}

// Compressed reports VARATT_EXTERNAL_IS_COMPRESSED.
func (p ToastPointer) Compressed() bool { return p.ExtSize < p.RawSize-4 }

const vartagOnDisk = 18

// ErrTruncated means a value runs past the end of its tuple.
var ErrTruncated = errors.New("datum: value runs past the end of the tuple")

// ParseVarlena decodes the varlena at b[0:].
func ParseVarlena(b []byte) (Varlena, error) {
	if len(b) < 1 {
		return Varlena{}, ErrTruncated
	}
	b0 := b[0]
	switch {
	case b0 == 0x01: // 1-byte header with vartag: external
		if len(b) < 2 {
			return Varlena{}, ErrTruncated
		}
		if b[1] != vartagOnDisk {
			return Varlena{}, fmt.Errorf("datum: unsupported vartag %d (in-memory pointer on disk?)", b[1])
		}
		if len(b) < 18 {
			return Varlena{}, ErrTruncated
		}
		le := binary.LittleEndian
		ext := le.Uint32(b[6:])
		p := ToastPointer{
			RawSize:    int32(le.Uint32(b[2:])),
			ExtSize:    int32(ext & 0x3FFFFFFF),
			Method:     int(ext >> 30),
			ValueID:    le.Uint32(b[10:]),
			ToastRelID: le.Uint32(b[14:]),
		}
		if p.RawSize < 4 || p.ExtSize < 0 {
			return Varlena{}, fmt.Errorf("datum: implausible TOAST pointer %+v", p)
		}
		return Varlena{Kind: VarExternal, Size: 18, Toast: p}, nil
	case b0&0x01 == 0x01: // 1-byte header
		n := int(b0 >> 1)
		if n < 1 || n > len(b) {
			return Varlena{}, ErrTruncated
		}
		return Varlena{Kind: VarPlain, Size: n, Data: b[1:n]}, nil
	}
	if len(b) < 4 {
		return Varlena{}, ErrTruncated
	}
	h := binary.LittleEndian.Uint32(b)
	n := int(h >> 2)
	if n < 4 || n > len(b) {
		return Varlena{}, ErrTruncated
	}
	if b0&0x03 == 0x00 {
		return Varlena{Kind: VarPlain, Size: n, Data: b[4:n]}, nil
	}
	if b0&0x03 == 0x02 {
		if n < 8 {
			return Varlena{}, ErrTruncated
		}
		tc := binary.LittleEndian.Uint32(b[4:])
		return Varlena{Kind: VarCompressed, Size: n, Data: b[8:n], RawSize: int(tc & 0x3FFFFFFF), Method: int(tc >> 30)}, nil
	}
	return Varlena{}, fmt.Errorf("datum: invalid varlena header byte 0x%02X", b0)
}

// Decompress inflates a compressed payload to exactly rawSize bytes.
func Decompress(method int, src []byte, rawSize int) ([]byte, error) {
	if rawSize < 0 || rawSize > 1<<30 {
		return nil, fmt.Errorf("datum: implausible raw size %d", rawSize)
	}
	switch method {
	case CompressPGLZ:
		return PGLZDecompress(src, rawSize)
	case CompressLZ4:
		return LZ4Decompress(src, rawSize)
	}
	return nil, fmt.Errorf("datum: unknown compression method %d", method)
}

// PGLZDecompress implements pglz_decompress (common/pg_lzcompress.c).
func PGLZDecompress(src []byte, rawSize int) ([]byte, error) {
	dst := make([]byte, 0, rawSize)
	sp := 0
	for sp < len(src) && len(dst) < rawSize {
		ctrl := src[sp]
		sp++
		for bit := 0; bit < 8 && sp < len(src) && len(dst) < rawSize; bit++ {
			if ctrl&1 != 0 {
				if sp+1 >= len(src) {
					return nil, errors.New("datum: pglz tag truncated")
				}
				length := int(src[sp]&0x0F) + 3
				off := int(src[sp]&0xF0)<<4 | int(src[sp+1])
				sp += 2
				if length == 18 {
					if sp >= len(src) {
						return nil, errors.New("datum: pglz length truncated")
					}
					length += int(src[sp])
					sp++
				}
				if off == 0 || off > len(dst) {
					return nil, fmt.Errorf("datum: pglz back-reference %d beyond %d output bytes", off, len(dst))
				}
				length = min(length, rawSize-len(dst))
				for i := 0; i < length; i++ { // overlapping copy is intentional
					dst = append(dst, dst[len(dst)-off])
				}
			} else {
				dst = append(dst, src[sp])
				sp++
			}
			ctrl >>= 1
		}
	}
	if len(dst) != rawSize {
		return nil, fmt.Errorf("datum: pglz produced %d bytes, expected %d", len(dst), rawSize)
	}
	return dst, nil
}

// LZ4Decompress implements LZ4 block-format decompression
// (LZ4_decompress_safe), which PostgreSQL uses for lz4 TOAST compression.
func LZ4Decompress(src []byte, rawSize int) ([]byte, error) {
	dst := make([]byte, 0, rawSize)
	sp := 0
	readLen := func(n int) (int, error) {
		if n != 15 {
			return n, nil
		}
		for {
			if sp >= len(src) {
				return 0, errors.New("datum: lz4 length truncated")
			}
			b := src[sp]
			sp++
			n += int(b)
			if n > rawSize+15 {
				return 0, errors.New("datum: lz4 length overflow")
			}
			if b != 255 {
				return n, nil
			}
		}
	}
	for sp < len(src) {
		token := src[sp]
		sp++
		lit, err := readLen(int(token >> 4))
		if err != nil {
			return nil, err
		}
		if sp+lit > len(src) || len(dst)+lit > rawSize {
			return nil, errors.New("datum: lz4 literal run out of bounds")
		}
		dst = append(dst, src[sp:sp+lit]...)
		sp += lit
		if sp >= len(src) {
			break // last sequence has literals only
		}
		if sp+1 >= len(src) {
			return nil, errors.New("datum: lz4 offset truncated")
		}
		off := int(src[sp]) | int(src[sp+1])<<8
		sp += 2
		if off == 0 || off > len(dst) {
			return nil, fmt.Errorf("datum: lz4 offset %d beyond %d output bytes", off, len(dst))
		}
		ml, err := readLen(int(token & 0x0F))
		if err != nil {
			return nil, err
		}
		ml += 4
		if len(dst)+ml > rawSize {
			return nil, errors.New("datum: lz4 match exceeds declared size")
		}
		for i := 0; i < ml; i++ {
			dst = append(dst, dst[len(dst)-off])
		}
	}
	if len(dst) != rawSize {
		return nil, fmt.Errorf("datum: lz4 produced %d bytes, expected %d", len(dst), rawSize)
	}
	return dst, nil
}
