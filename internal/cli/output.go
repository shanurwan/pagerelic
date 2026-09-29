package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shanurwan/pagerelic/internal/buildinfo"
	"github.com/shanurwan/pagerelic/internal/recover"
	"github.com/shanurwan/pagerelic/internal/schema"
)

// createExclusive creates a new file, refusing to overwrite anything.
func createExclusive(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("%s already exists; refusing to overwrite recovery output", path)
	}
	return f, err
}

// rowWriter emits rows as JSON lines or CSV.
type rowWriter struct {
	format string
	w      io.Writer
	csv    *csv.Writer
	cols   []schema.Column
	h      *hashWriter
}

type hashWriter struct {
	w io.Writer
	n int64
	s interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
}

func (h *hashWriter) Write(p []byte) (int, error) {
	n, err := h.w.Write(p)
	h.s.Write(p[:n])
	h.n += int64(n)
	return n, err
}

func newRowWriter(format string, w io.Writer, cols []schema.Column) (*rowWriter, error) {
	hw := &hashWriter{w: w, s: sha256.New()}
	rw := &rowWriter{format: format, w: hw, cols: schema.Visible(cols), h: hw}
	switch format {
	case "jsonl":
	case "csv":
		rw.csv = csv.NewWriter(hw)
		head := []string{"_block", "_lp", "_offset", "_source", "_state", "_confidence", "_xmin", "_xmax", "_ctid"}
		for _, c := range rw.cols {
			head = append(head, c.Name)
		}
		head = append(head, "_notes")
		if err := rw.csv.Write(head); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown --format %q (jsonl or csv)", format)
	}
	return rw, nil
}

func (rw *rowWriter) write(r recover.Row) error {
	notes := append([]string(nil), r.Notes...)
	for i, v := range r.Values {
		if v.Err != "" {
			notes = append(notes, rw.cols[i].Name+": "+v.Err)
		}
	}
	if rw.csv != nil {
		rec := []string{strconv.FormatUint(uint64(r.Block), 10), strconv.Itoa(r.Item), strconv.Itoa(r.Offset), r.Source, string(r.State),
			r.Confidence, strconv.FormatUint(uint64(r.Xmin), 10), strconv.FormatUint(uint64(r.Xmax), 10), r.Ctid}
		for _, v := range r.Values {
			if v.Null {
				rec = append(rec, `\N`)
			} else {
				rec = append(rec, v.Text)
			}
		}
		rec = append(rec, strings.Join(notes, "; "))
		return rw.csv.Write(rec)
	}
	var b bytes.Buffer
	field := func(k string, v any) {
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(v)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('{')
	field("_block", r.Block)
	field("_lp", r.Item)
	field("_offset", r.Offset)
	field("_source", r.Source)
	field("_state", r.State)
	field("_confidence", r.Confidence)
	field("_xmin", r.Xmin)
	field("_xmax", r.Xmax)
	field("_ctid", r.Ctid)
	if r.Evidence != "" {
		field("_evidence", r.Evidence)
	}
	for i, v := range r.Values {
		if v.Null || (v.Err != "" && v.Text == "") {
			field(rw.cols[i].Name, nil)
		} else {
			field(rw.cols[i].Name, v.Text)
		}
	}
	if len(notes) > 0 {
		field("_notes", notes)
	}
	b.WriteString("}\n")
	_, err := rw.w.Write(b.Bytes())
	return err
}

func (rw *rowWriter) close() error {
	if rw.csv != nil {
		rw.csv.Flush()
		return rw.csv.Error()
	}
	return nil
}

func (rw *rowWriter) digest() string { return hex.EncodeToString(rw.h.s.Sum(nil)) }

// fileDigest describes an input for the manifest.
type fileDigest struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func digestFile(p string) (fileDigest, error) {
	f, err := os.Open(p)
	if err != nil {
		return fileDigest{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return fileDigest{Path: p, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, err
}

type manifest struct {
	Tool     buildinfo.Info  `json:"tool"`
	Command  []string        `json:"command"`
	Started  time.Time       `json:"started"`
	Finished time.Time       `json:"finished"`
	Inputs   []fileDigest    `json:"inputs"`
	Output   fileDigest      `json:"output"`
	Columns  []schema.Column `json:"columns"`
	Summary  any             `json:"summary"`
}

func writeManifest(path string, m manifest) error {
	f, err := createExclusive(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
