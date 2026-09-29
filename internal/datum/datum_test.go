package datum

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func i16(v ...int16) []byte {
	b := make([]byte, 2*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(x))
	}
	return b
}

func TestNumericFormats(t *testing.T) {
	short := func(neg bool, dscale, weight int, digits ...int16) []byte {
		h := uint16(0x8000) | uint16(dscale)<<7 | uint16(weight&0x3F)
		if weight < 0 {
			h |= 0x0040
		}
		if neg {
			h |= 0x2000
		}
		return append(i16(int16(h)), i16(digits...)...)
	}
	long := func(sign uint16, dscale int, weight int16, digits ...int16) []byte {
		return append(i16(int16(sign|uint16(dscale)), weight), i16(digits...)...)
	}
	cases := []struct {
		in   []byte
		want string
	}{
		{short(false, 0, 0), "0"},
		{short(false, 2, 0, 1234, 5000), "1234.50"},
		{short(true, 2, 0, 42), "-42.00"},
		{short(false, 6, -1, 1, 2300), "0.000123"}, // base-10000 groups 0001|2300
		{short(false, 9, 2, 1, 2345, 6789, 1234, 5678, 9000), "123456789.123456789"},
		{long(0x0000, 0, 1, 12, 3456), "123456"},
		{long(0x4000, 3, 0, 7, 1250), "-7.125"},
		{i16(-16384), "NaN"},      // 0xC000
		{i16(-12288), "Infinity"}, // 0xD000
		{i16(-4096), "-Infinity"}, // 0xF000
	}
	for _, c := range cases {
		got, err := fmtNumeric(c.in)
		if err != nil || got != c.want {
			t.Errorf("numeric % x = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestFloatOutput(t *testing.T) {
	cases := []struct {
		f    float64
		bits int
		want string
	}{
		{1.0 / 3, 64, "0.3333333333333333"},
		{1e15, 64, "1e+15"},
		{123456789012345, 64, "123456789012345"},
		{0.0001, 64, "0.0001"},
		{0.00001, 64, "1e-05"},
		{-2.5, 64, "-2.5"},
		{float64(float32(1234567)), 32, "1.234567e+06"},
		{float64(float32(123456)), 32, "123456"},
	}
	for _, c := range cases {
		max := 15
		if c.bits == 32 {
			max = 6
		}
		if got := formatFloat(c.f, c.bits, max); got != c.want {
			t.Errorf("%v (%d-bit) = %q, want %q", c.f, c.bits, got, c.want)
		}
	}
}

func TestIntervalOutput(t *testing.T) {
	iv := func(us int64, days, months int32) []byte {
		b := make([]byte, 16)
		binary.LittleEndian.PutUint64(b, uint64(us))
		binary.LittleEndian.PutUint32(b[8:], uint32(days))
		binary.LittleEndian.PutUint32(b[12:], uint32(months))
		return b
	}
	cases := []struct {
		in   []byte
		want string
	}{
		{iv(0, 0, 0), "00:00:00"},
		{iv(3723250000, 2, 14), "1 year 2 mons 2 days 01:02:03.25"},
		{iv(0, 1, 1), "1 mon 1 day"},
		{iv(-3600e6, 1, 0), "1 day -01:00:00"},
		{iv(3600e6, -1, 0), "-1 days +01:00:00"},
	}
	for _, c := range cases {
		if got, _ := fmtInterval(c.in); got != c.want {
			t.Errorf("interval = %q, want %q", got, c.want)
		}
	}
}

func TestDateAndTimestampEdges(t *testing.T) {
	d := make([]byte, 4)
	binary.LittleEndian.PutUint32(d, uint32(0x7FFFFFFF))
	if s, _ := fmtDate(d); s != "infinity" {
		t.Fatal(s)
	}
	bc := int32(-730486) // days from 2000-01-01 back into 1 BC
	binary.LittleEndian.PutUint32(d, uint32(bc))
	if s, _ := fmtDate(d); len(s) < 10 || s[len(s)-2:] != "BC" {
		t.Fatalf("BC date = %q", s)
	}
	ts := make([]byte, 8)
	minusOne := int64(-1)
	binary.LittleEndian.PutUint64(ts, uint64(minusOne)) // 1999-12-31 23:59:59.999999
	if s, _ := fmtTimestamp(ts); s != "1999-12-31 23:59:59.999999" {
		t.Fatalf("timestamp = %q", s)
	}
}

func TestArrayQuoting(t *testing.T) {
	for in, want := range map[string]string{"a": "a", "": `""`, "NULL": `"NULL"`, "a b": `"a b"`, `x"y`: `"x\"y"`, "{": `"{"`} {
		if got := quoteArrayElem(in); got != want {
			t.Errorf("quote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPGLZRoundTripShapes(t *testing.T) {
	// ctrl byte 0b00000010: literal 'a', then match len 3+15=18 → extended.
	src := []byte{0x02, 'a', 0x0F, 0x01, 0x05}
	got, err := PGLZDecompress(src, 1+23)
	if err != nil || !bytes.Equal(got, bytes.Repeat([]byte("a"), 24)) {
		t.Fatalf("pglz = %q, %v", got, err)
	}
	if _, err := PGLZDecompress([]byte{0x01, 0x0F, 0xFF}, 10); err == nil {
		t.Fatal("back-reference before start accepted")
	}
}

func TestLZ4Shapes(t *testing.T) {
	// token: 1 literal, match len 4+4=8, offset 1 → "a" × 9, then final literal "b".
	src := []byte{0x14, 'a', 0x01, 0x00, 0x10, 'b'}
	got, err := LZ4Decompress(src, 10)
	if err != nil || string(got) != "aaaaaaaaab" {
		t.Fatalf("lz4 = %q, %v", got, err)
	}
	if _, err := LZ4Decompress([]byte{0x14, 'a', 0x09, 0x00}, 10); err == nil {
		t.Fatal("offset beyond output accepted")
	}
}

func FuzzDecompress(f *testing.F) {
	f.Add([]byte{0x02, 'a', 0x0F, 0x01, 0x05}, 24)
	f.Add([]byte{0x14, 'a', 0x01, 0x00, 0x10, 'b'}, 10)
	f.Fuzz(func(t *testing.T, src []byte, n int) {
		if n < 0 || n > 1<<16 {
			return
		}
		if b, err := PGLZDecompress(src, n); err == nil && len(b) != n {
			t.Fatal("pglz returned wrong length without error")
		}
		if b, err := LZ4Decompress(src, n); err == nil && len(b) != n {
			t.Fatal("lz4 returned wrong length without error")
		}
	})
}

func FuzzFormat(f *testing.F) {
	f.Add(uint32(OIDNumeric), i16(-32768+0x0100, 1234))
	f.Add(uint32(1009), []byte{1, 0, 0, 0, 0, 0, 0, 0, 25, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 11, 'a', 'b', 'c', 'd', 'e'})
	f.Fuzz(func(t *testing.T, oid uint32, b []byte) {
		_, _ = Format(oid%4000, b)
		_, _ = ParseVarlena(b)
	})
}
