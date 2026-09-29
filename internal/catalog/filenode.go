// Package catalog reads PostgreSQL's system catalogs straight from their
// heap files, without a running server, to discover relations, their
// files, and their column layouts, including those of dropped tables,
// whose catalog rows survive as deleted tuples until VACUUM.
package catalog

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
)

// Mapped catalogs have relfilenode 0 in pg_class; their files are named in
// pg_filenode.map.
const (
	OIDPgType      = 1247
	OIDPgAttribute = 1249
	OIDPgClass     = 1259
	OIDPgDatabase  = 1262
	OIDPgNamespace = 2615
	relMapMagic    = 0x592717
)

// FileNodeMap maps catalog OID → relfilenode.
type FileNodeMap map[uint32]uint32

// ReadFileNodeMap parses and CRC-checks a pg_filenode.map file. The layout
// is {int32 magic; int32 n; {oid, filenode}[MAX]; crc32c}; MAX_MAPPINGS
// is 64 in PostgreSQL 16+ (524-byte file) and 62 before (512 bytes).
func ReadFileNodeMap(path string) (FileNodeMap, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseFileNodeMap(b)
}

// ParseFileNodeMap parses pg_filenode.map bytes.
func ParseFileNodeMap(b []byte) (FileNodeMap, error) {
	var maxMappings int
	switch len(b) {
	case 524:
		maxMappings = 64
	case 512:
		maxMappings = 62
	default:
		return nil, fmt.Errorf("catalog: pg_filenode.map has unexpected size %d", len(b))
	}
	le := binary.LittleEndian
	if le.Uint32(b) != relMapMagic {
		return nil, fmt.Errorf("catalog: pg_filenode.map magic 0x%X", le.Uint32(b))
	}
	n := int(le.Uint32(b[4:]))
	if n < 0 || n > maxMappings {
		return nil, fmt.Errorf("catalog: pg_filenode.map claims %d mappings", n)
	}
	crcOff := 8 + 8*maxMappings
	if want, got := le.Uint32(b[crcOff:]), crc32.Checksum(b[:crcOff], crc32.MakeTable(crc32.Castagnoli)); want != got {
		return nil, fmt.Errorf("catalog: pg_filenode.map CRC mismatch (stored 0x%08X, computed 0x%08X)", want, got)
	}
	m := FileNodeMap{}
	for i := 0; i < n; i++ {
		m[le.Uint32(b[8+8*i:])] = le.Uint32(b[12+8*i:])
	}
	return m, nil
}
