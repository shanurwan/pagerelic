package datum

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// ErrUnsupported marks values rendered as hex because the type has no
// text decoder yet.
var ErrUnsupported = errors.New("datum: type not decoded; value shown as hex")

var le = binary.LittleEndian

// Format renders a value's payload as PostgreSQL text output. For varlena
// types the payload excludes the header and must already be detoasted and
// decompressed. Session settings assumed: TimeZone=UTC, DateStyle=ISO,
// IntervalStyle=postgres, extra_float_digits=1 (the PostgreSQL 12+ default).
func Format(oid uint32, payload []byte) (string, error) {
	t, ok := builtins[oid]
	if !ok {
		return `\x` + hex.EncodeToString(payload), ErrUnsupported
	}
	if t.Elem != 0 {
		return formatArray(t, payload)
	}
	if t.format == nil {
		return `\x` + hex.EncodeToString(payload), ErrUnsupported
	}
	if t.Len > 0 && len(payload) < int(t.Len) {
		return "", ErrTruncated
	}
	return t.format(payload)
}

func fmtBool(b []byte) (string, error) {
	if b[0] != 0 {
		return "t", nil
	}
	return "f", nil
}

func fmtBytea(b []byte) (string, error) { return `\x` + hex.EncodeToString(b), nil }

func fmtChar(b []byte) (string, error) {
	switch c := b[0]; {
	case c == 0:
		return "", nil
	case c >= 0x80:
		return fmt.Sprintf(`\%03o`, c), nil
	default:
		return string(rune(c)), nil
	}
}

func fmtName(b []byte) (string, error) {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return string(b), nil
}

func fmtText(b []byte) (string, error) { return string(b), nil }
func fmtInt2(b []byte) (string, error) { return strconv.Itoa(int(int16(le.Uint16(b)))), nil }
func fmtInt4(b []byte) (string, error) { return strconv.Itoa(int(int32(le.Uint32(b)))), nil }
func fmtInt8(b []byte) (string, error) {
	return strconv.FormatInt(int64(le.Uint64(b)), 10), nil
}
func fmtUint4(b []byte) (string, error) { return strconv.FormatUint(uint64(le.Uint32(b)), 10), nil }

func fmtTid(b []byte) (string, error) {
	blk := uint32(le.Uint16(b[0:]))<<16 | uint32(le.Uint16(b[2:]))
	return fmt.Sprintf("(%d,%d)", blk, le.Uint16(b[4:])), nil
}

func fmtLSN(b []byte) (string, error) {
	v := le.Uint64(b)
	return fmt.Sprintf("%X/%X", uint32(v>>32), uint32(v)), nil
}

func fmtUUID(b []byte) (string, error) {
	h := hex.EncodeToString(b[:16])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

// Floats: PostgreSQL 12+ prints the shortest exact representation, in
// fixed notation when the decimal exponent is in [-4, 15) for float8 and
// [-4, 6) for float4, otherwise in exponential notation.
func fmtFloat4(b []byte) (string, error) {
	return formatFloat(float64(math.Float32frombits(le.Uint32(b))), 32, 6), nil
}

func fmtFloat8(b []byte) (string, error) {
	return formatFloat(math.Float64frombits(le.Uint64(b)), 64, 15), nil
}

func formatFloat(f float64, bits, fixedMax int) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	e := strconv.FormatFloat(f, 'e', -1, bits) // e.g. -1.2345e+02
	neg := strings.HasPrefix(e, "-")
	e = strings.TrimPrefix(e, "-")
	mant, expStr, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expStr)
	if exp < -4 || exp >= fixedMax {
		if neg {
			return "-" + e
		}
		return e
	}
	digits := strings.Replace(mant, ".", "", 1)
	var s string
	if exp >= 0 {
		if len(digits) <= exp+1 {
			s = digits + strings.Repeat("0", exp+1-len(digits))
		} else {
			s = digits[:exp+1] + "." + digits[exp+1:]
		}
	} else {
		s = "0." + strings.Repeat("0", -exp-1) + digits
	}
	if neg {
		s = "-" + s
	}
	return s
}

// Date/time: PostgreSQL counts from 2000-01-01 (POSTGRES_EPOCH).
var pgEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

func formatYMD(t time.Time) (string, bool) {
	y := t.Year()
	bc := y <= 0
	if bc {
		y = 1 - y
	}
	return fmt.Sprintf("%04d-%02d-%02d", y, int(t.Month()), t.Day()), bc
}

func fmtDate(b []byte) (string, error) {
	d := int32(le.Uint32(b))
	switch d {
	case math.MinInt32:
		return "-infinity", nil
	case math.MaxInt32:
		return "infinity", nil
	}
	s, bc := formatYMD(pgEpoch.AddDate(0, 0, int(d)))
	if bc {
		s += " BC"
	}
	return s, nil
}

func appendFraction(s string, usec int64) string {
	if usec == 0 {
		return s
	}
	return s + "." + strings.TrimRight(fmt.Sprintf("%06d", usec), "0")
}

func timestampText(us int64, tz bool) string {
	switch us {
	case math.MinInt64:
		return "-infinity"
	case math.MaxInt64:
		return "infinity"
	}
	sec, frac := us/1e6, us%1e6
	if frac < 0 {
		sec--
		frac += 1e6
	}
	t := pgEpoch.Add(time.Duration(sec) * time.Second)
	ymd, bc := formatYMD(t)
	s := appendFraction(fmt.Sprintf("%s %02d:%02d:%02d", ymd, t.Hour(), t.Minute(), t.Second()), frac)
	if tz {
		s += "+00"
	}
	if bc {
		s += " BC"
	}
	return s
}

func fmtTimestamp(b []byte) (string, error)   { return timestampText(int64(le.Uint64(b)), false), nil }
func fmtTimestamptz(b []byte) (string, error) { return timestampText(int64(le.Uint64(b)), true), nil }

func fmtTime(b []byte) (string, error) {
	us := int64(le.Uint64(b))
	h, m, s := us/3600e6, us/60e6%60, us/1e6%60
	return appendFraction(fmt.Sprintf("%02d:%02d:%02d", h, m, s), us%1e6), nil
}

// fmtInterval implements EncodeInterval for IntervalStyle=postgres.
func fmtInterval(b []byte) (string, error) {
	us := int64(le.Uint64(b[0:]))
	days := int64(int32(le.Uint32(b[8:])))
	months := int64(int32(le.Uint32(b[12:])))
	var sb strings.Builder
	isZero, isBefore := true, false
	part := func(v int64, unit string) {
		if v == 0 {
			return
		}
		if !isZero {
			sb.WriteByte(' ')
		}
		if isBefore && v > 0 {
			sb.WriteByte('+')
		}
		fmt.Fprintf(&sb, "%d %s", v, unit)
		if v != 1 {
			sb.WriteByte('s')
		}
		isBefore, isZero = v < 0, false
	}
	part(months/12, "year")
	part(months%12, "mon")
	part(days, "day")
	hour, rem := us/3600e6, us%3600e6
	min, rem := rem/60e6, rem%60e6
	sec, fsec := rem/1e6, rem%1e6
	if isZero || hour != 0 || min != 0 || sec != 0 || fsec != 0 {
		minus := hour < 0 || min < 0 || sec < 0 || fsec < 0
		if !isZero {
			sb.WriteByte(' ')
		}
		switch {
		case minus:
			sb.WriteByte('-')
		case isBefore:
			sb.WriteByte('+')
		}
		abs := func(v int64) int64 {
			if v < 0 {
				return -v
			}
			return v
		}
		sb.WriteString(appendFraction(fmt.Sprintf("%02d:%02d:%02d", abs(hour), abs(min), abs(sec)), abs(fsec)))
	}
	return sb.String(), nil
}

// fmtNumeric decodes NumericData (utils/adt/numeric.c) in both the short
// and long header formats, plus NaN and ±Infinity.
func fmtNumeric(b []byte) (string, error) {
	if len(b) < 2 {
		return "", ErrTruncated
	}
	h := le.Uint16(b)
	var neg bool
	var dscale, weight int
	var digits []byte
	switch {
	case h&0xC000 == 0xC000:
		switch h & 0xF000 {
		case 0xC000:
			return "NaN", nil
		case 0xD000:
			return "Infinity", nil
		case 0xF000:
			return "-Infinity", nil
		}
		return "", fmt.Errorf("datum: unknown special numeric 0x%04X", h)
	case h&0x8000 != 0: // short format
		neg = h&0x2000 != 0
		dscale = int(h&0x1F80) >> 7
		weight = int(h & 0x3F)
		if h&0x0040 != 0 {
			weight |= ^0x3F
		}
		digits = b[2:]
	default:
		if len(b) < 4 {
			return "", ErrTruncated
		}
		neg = h&0xC000 == 0x4000
		dscale = int(h & 0x3FFF)
		weight = int(int16(le.Uint16(b[2:])))
		digits = b[4:]
	}
	if len(digits)%2 != 0 {
		return "", fmt.Errorf("datum: numeric digit array has odd length %d", len(digits))
	}
	n := len(digits) / 2
	digit := func(i int) int {
		if i < 0 || i >= n {
			return 0
		}
		return int(int16(le.Uint16(digits[2*i:])))
	}
	for i := 0; i < n; i++ {
		if d := digit(i); d < 0 || d >= 10000 {
			return "", fmt.Errorf("datum: numeric digit %d out of range", d)
		}
	}
	var sb strings.Builder
	if neg {
		sb.WriteByte('-')
	}
	if weight < 0 {
		sb.WriteByte('0')
	} else {
		for d := 0; d <= weight; d++ {
			if d == 0 {
				sb.WriteString(strconv.Itoa(digit(d)))
			} else {
				fmt.Fprintf(&sb, "%04d", digit(d))
			}
		}
	}
	if dscale > 0 {
		var frac strings.Builder
		for d := weight + 1; frac.Len() < dscale; d++ {
			fmt.Fprintf(&frac, "%04d", digit(d))
		}
		sb.WriteByte('.')
		sb.WriteString(frac.String()[:dscale])
	}
	return sb.String(), nil
}

// formatArray decodes ArrayType (utils/array.h) and renders array_out text.
// Offsets inside an array are relative to its 4-byte-header form, so every
// alignment is computed on payload position + 4.
func formatArray(t *Type, p []byte) (string, error) {
	if len(p) < 12 {
		return "", ErrTruncated
	}
	ndim := int(int32(le.Uint32(p[0:])))
	dataOffset := int(int32(le.Uint32(p[4:])))
	elemOID := le.Uint32(p[8:])
	if ndim == 0 {
		return "{}", nil
	}
	if ndim < 0 || ndim > 6 || len(p) < 12+8*ndim {
		return "", fmt.Errorf("datum: implausible array with %d dimensions", ndim)
	}
	dims := make([]int, ndim)
	lbs := make([]int, ndim)
	nitems := 1
	for i := 0; i < ndim; i++ {
		dims[i] = int(int32(le.Uint32(p[12+4*i:])))
		lbs[i] = int(int32(le.Uint32(p[12+4*ndim+4*i:])))
		if dims[i] < 0 || dims[i] > 1<<24 {
			return "", fmt.Errorf("datum: implausible array dimension %d", dims[i])
		}
		nitems *= dims[i]
		if nitems > 1<<24 {
			return "", errors.New("datum: array too large")
		}
	}
	pos := 12 + 8*ndim
	var nulls []byte
	if dataOffset != 0 {
		nb := (nitems + 7) / 8
		if pos+nb > len(p) {
			return "", ErrTruncated
		}
		nulls = p[pos : pos+nb]
		pos = dataOffset - 4
	} else {
		pos = maxAlign(pos+4) - 4
	}
	if elemOID != t.Elem && t.Elem != 0 {
		return "", fmt.Errorf("datum: array element type %d does not match column type %s", elemOID, t.Name)
	}
	et, ok := builtins[elemOID]
	if !ok {
		return "", ErrUnsupported
	}
	items := make([]string, nitems)
	isNull := make([]bool, nitems)
	for i := 0; i < nitems; i++ {
		if nulls != nil && nulls[i/8]&(1<<(i%8)) == 0 {
			isNull[i] = true
			continue
		}
		pos = alignTo(pos+4, et.Align) - 4
		if pos < 0 || pos > len(p) {
			return "", ErrTruncated
		}
		var payload []byte
		switch {
		case et.Len > 0:
			if pos+int(et.Len) > len(p) {
				return "", ErrTruncated
			}
			payload = p[pos : pos+int(et.Len)]
			pos += int(et.Len)
		case et.Len == -1:
			v, err := ParseVarlena(p[pos:])
			if err != nil {
				return "", err
			}
			if v.Kind != VarPlain {
				return "", errors.New("datum: compressed or external array element")
			}
			payload = v.Data
			pos += v.Size
		default:
			return "", ErrUnsupported
		}
		s, err := Format(elemOID, payload)
		if err != nil {
			return "", err
		}
		items[i] = s
	}
	if t.OID == OIDInt2Vector || t.OID == OIDOidVector {
		return strings.Join(items, " "), nil
	}
	var sb strings.Builder
	for i := 0; i < ndim; i++ {
		if lbs[i] != 1 {
			for j := 0; j < ndim; j++ {
				fmt.Fprintf(&sb, "[%d:%d]", lbs[j], lbs[j]+dims[j]-1)
			}
			sb.WriteByte('=')
			break
		}
	}
	idx := 0
	var emit func(d int)
	emit = func(d int) {
		sb.WriteByte('{')
		for k := 0; k < dims[d]; k++ {
			if k > 0 {
				sb.WriteByte(',')
			}
			if d+1 < ndim {
				emit(d + 1)
				continue
			}
			if isNull[idx] {
				sb.WriteString("NULL")
			} else {
				sb.WriteString(quoteArrayElem(items[idx]))
			}
			idx++
		}
		sb.WriteByte('}')
	}
	emit(0)
	return sb.String(), nil
}

func quoteArrayElem(s string) string {
	need := s == "" || strings.EqualFold(s, "NULL")
	for _, r := range s {
		if strings.ContainsRune("{}\",\\", r) || r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' {
			need = true
			break
		}
	}
	if !need {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

func maxAlign(n int) int { return (n + 7) &^ 7 }

// alignTo applies typalign to an offset.
func alignTo(n int, align byte) int {
	switch align {
	case 's':
		return (n + 1) &^ 1
	case 'i':
		return (n + 3) &^ 3
	case 'd':
		return (n + 7) &^ 7
	}
	return n
}

// AlignTo is exported for the tuple decoder.
func AlignTo(n int, align byte) int { return alignTo(n, align) }
