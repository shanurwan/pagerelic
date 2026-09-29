// Package schema describes the column layout needed to decode tuples:
// either parsed from a user-supplied spec or read from the system catalogs.
package schema

import (
	"fmt"
	"strings"

	"github.com/shanurwan/pagerelic/internal/datum"
)

// Column is the subset of pg_attribute that tuple decoding needs.
type Column struct {
	Name     string `json:"name"`
	TypeOID  uint32 `json:"type_oid"`
	TypeName string `json:"type"`
	Len      int16  `json:"len"`   // attlen
	Align    byte   `json:"align"` // attalign
	ByVal    bool   `json:"byval"`
	TypMod   int32  `json:"typmod,omitempty"`
	Dropped  bool   `json:"dropped,omitempty"`
}

// Parse reads a comma-separated column spec such as
// "id int4, name text, balance numeric(12,2), tags text[]". Commas inside
// parentheses belong to type modifiers.
func Parse(spec string) ([]Column, error) {
	var parts []string
	depth, start := 0, 0
	for i, r := range spec {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, spec[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, spec[start:])
	var cols []Column
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		name, typ, ok := strings.Cut(p, " ")
		if !ok {
			return nil, fmt.Errorf("schema: %q needs a name and a type", p)
		}
		name = strings.Trim(name, `"`)
		t, ok := datum.LookupName(typ)
		if !ok {
			return nil, fmt.Errorf("schema: unknown type %q for column %s (use --catalog for custom types)", strings.TrimSpace(typ), name)
		}
		cols = append(cols, Column{Name: name, TypeOID: t.OID, TypeName: strings.TrimSpace(typ), Len: t.Len, Align: t.Align, ByVal: t.ByVal})
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("schema: empty column list")
	}
	return cols, nil
}

// Visible returns the columns that are not dropped.
func Visible(cols []Column) []Column {
	var out []Column
	for _, c := range cols {
		if !c.Dropped {
			out = append(out, c)
		}
	}
	return out
}

// String renders a spec that Parse accepts (for built-in types).
func String(cols []Column) string {
	var parts []string
	for _, c := range cols {
		if !c.Dropped {
			parts = append(parts, c.Name+" "+c.TypeName)
		}
	}
	return strings.Join(parts, ", ")
}
