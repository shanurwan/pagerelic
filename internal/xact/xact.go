// Package xact reads the commit log (pg_xact, formerly pg_clog): two bits
// per transaction ID recording in-progress / committed / aborted /
// sub-committed. With it, tuples whose hint bits were never set can still
// be classified, which is common for recently changed rows.
package xact

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/shanurwan/pagerelic/internal/heap"
)

const (
	xactsPerByte     = 4
	pagesPerSegment  = 32
	defaultBlockSize = 8192
)

// Log is a loaded commit log.
type Log struct {
	blockSize int
	segments  map[int64][]byte
}

// Open loads every segment file in a pg_xact directory.
func Open(dir string) (*Log, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("xact: %w", err)
	}
	l := &Log{blockSize: defaultBlockSize, segments: map[int64][]byte{}}
	for _, e := range entries {
		n, err := strconv.ParseInt(e.Name(), 16, 64)
		if err != nil || e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("xact: %w", err)
		}
		l.segments[n] = b
	}
	if len(l.segments) == 0 {
		return nil, fmt.Errorf("xact: no segment files in %s", dir)
	}
	return l, nil
}

// FromSegment builds a Log from one segment's bytes (tests, carved data).
func FromSegment(segno int64, b []byte) *Log {
	return &Log{blockSize: defaultBlockSize, segments: map[int64][]byte{segno: b}}
}

// Status implements heap.XactLookup. Special XIDs 1 and 2 are committed;
// XIDs outside the loaded segments are unknown. A zero status in a segment
// means "in progress" only up to the end of the file: pages beyond it were
// never written.
func (l *Log) Status(xid uint32) heap.XactStatus {
	if xid == heap.BootstrapXID || xid == heap.FrozenXID {
		return heap.StatusCommitted
	}
	if xid < 3 {
		return heap.StatusUnknown
	}
	perPage := int64(l.blockSize * xactsPerByte)
	perSeg := perPage * pagesPerSegment
	seg := int64(xid) / perSeg
	b, ok := l.segments[seg]
	if !ok {
		return heap.StatusUnknown
	}
	byteIdx := (int64(xid) % perSeg) / xactsPerByte
	if byteIdx >= int64(len(b)) {
		return heap.StatusUnknown
	}
	shift := uint(xid%xactsPerByte) * 2
	return heap.XactStatus(b[byteIdx] >> shift & 0x3)
}
