package datum

import (
	"strings"
)

// Type describes a built-in type: storage properties and text output.
type Type struct {
	OID    uint32
	Name   string
	Len    int16 // attlen: >0 fixed, -1 varlena, -2 cstring
	Align  byte  // 'c', 's', 'i', 'd'
	ByVal  bool
	Elem   uint32 // element type for arrays
	format func(b []byte) (string, error)
}

// Well-known OIDs.
const (
	OIDBool        = 16
	OIDBytea       = 17
	OIDChar        = 18
	OIDName        = 19
	OIDInt8        = 20
	OIDInt2        = 21
	OIDInt2Vector  = 22
	OIDInt4        = 23
	OIDRegproc     = 24
	OIDText        = 25
	OIDOid         = 26
	OIDTid         = 27
	OIDXid         = 28
	OIDCid         = 29
	OIDOidVector   = 30
	OIDPgNodeTree  = 194
	OIDJSON        = 114
	OIDXML         = 142
	OIDFloat4      = 700
	OIDFloat8      = 701
	OIDMoney       = 790
	OIDBpchar      = 1042
	OIDVarchar     = 1043
	OIDDate        = 1082
	OIDTime        = 1083
	OIDTimestamp   = 1114
	OIDTimestamptz = 1184
	OIDInterval    = 1186
	OIDTimetz      = 1266
	OIDNumeric     = 1700
	OIDRegclass    = 2205
	OIDUUID        = 2950
	OIDJSONB       = 3802
	OIDAclitem     = 1033
	OIDAnyArray    = 2277
	OIDPgLSN       = 3220
	OIDXid8        = 5069
)

var builtins = map[uint32]*Type{}
var byName = map[string]uint32{}

func reg(t *Type, aliases ...string) {
	builtins[t.OID] = t
	byName[t.Name] = t.OID
	for _, a := range aliases {
		byName[a] = t.OID
	}
}

func arr(oid uint32, name string, elem uint32, align byte) {
	reg(&Type{OID: oid, Name: name, Len: -1, Align: align, Elem: elem})
}

func init() {
	reg(&Type{OID: OIDBool, Name: "bool", Len: 1, Align: 'c', ByVal: true, format: fmtBool}, "boolean")
	reg(&Type{OID: OIDBytea, Name: "bytea", Len: -1, Align: 'i', format: fmtBytea})
	reg(&Type{OID: OIDChar, Name: `"char"`, Len: 1, Align: 'c', ByVal: true, format: fmtChar}, "char1")
	reg(&Type{OID: OIDName, Name: "name", Len: 64, Align: 'c', format: fmtName})
	reg(&Type{OID: OIDInt8, Name: "int8", Len: 8, Align: 'd', ByVal: true, format: fmtInt8}, "bigint")
	reg(&Type{OID: OIDInt2, Name: "int2", Len: 2, Align: 's', ByVal: true, format: fmtInt2}, "smallint")
	reg(&Type{OID: OIDInt4, Name: "int4", Len: 4, Align: 'i', ByVal: true, format: fmtInt4}, "int", "integer")
	reg(&Type{OID: OIDRegproc, Name: "regproc", Len: 4, Align: 'i', ByVal: true, format: fmtUint4})
	reg(&Type{OID: OIDText, Name: "text", Len: -1, Align: 'i', format: fmtText})
	reg(&Type{OID: OIDOid, Name: "oid", Len: 4, Align: 'i', ByVal: true, format: fmtUint4})
	reg(&Type{OID: OIDTid, Name: "tid", Len: 6, Align: 's', format: fmtTid})
	reg(&Type{OID: OIDXid, Name: "xid", Len: 4, Align: 'i', ByVal: true, format: fmtUint4})
	reg(&Type{OID: OIDCid, Name: "cid", Len: 4, Align: 'i', ByVal: true, format: fmtUint4})
	reg(&Type{OID: OIDRegclass, Name: "regclass", Len: 4, Align: 'i', ByVal: true, format: fmtUint4})
	reg(&Type{OID: OIDPgNodeTree, Name: "pg_node_tree", Len: -1, Align: 'i', format: fmtText})
	reg(&Type{OID: OIDJSON, Name: "json", Len: -1, Align: 'i', format: fmtText})
	reg(&Type{OID: OIDXML, Name: "xml", Len: -1, Align: 'i', format: fmtText})
	reg(&Type{OID: OIDFloat4, Name: "float4", Len: 4, Align: 'i', ByVal: true, format: fmtFloat4}, "real")
	reg(&Type{OID: OIDFloat8, Name: "float8", Len: 8, Align: 'd', ByVal: true, format: fmtFloat8}, "double precision", "double")
	// money output depends on lc_monetary; shown as hex rather than guessed.
	reg(&Type{OID: OIDMoney, Name: "money", Len: 8, Align: 'd', ByVal: true})
	reg(&Type{OID: OIDBpchar, Name: "bpchar", Len: -1, Align: 'i', format: fmtText}, "char", "character")
	reg(&Type{OID: OIDVarchar, Name: "varchar", Len: -1, Align: 'i', format: fmtText}, "character varying")
	reg(&Type{OID: OIDDate, Name: "date", Len: 4, Align: 'i', ByVal: true, format: fmtDate})
	reg(&Type{OID: OIDTime, Name: "time", Len: 8, Align: 'd', ByVal: true, format: fmtTime}, "time without time zone")
	reg(&Type{OID: OIDTimestamp, Name: "timestamp", Len: 8, Align: 'd', ByVal: true, format: fmtTimestamp}, "timestamp without time zone")
	reg(&Type{OID: OIDTimestamptz, Name: "timestamptz", Len: 8, Align: 'd', ByVal: true, format: fmtTimestamptz}, "timestamp with time zone")
	reg(&Type{OID: OIDInterval, Name: "interval", Len: 16, Align: 'd', format: fmtInterval})
	reg(&Type{OID: OIDNumeric, Name: "numeric", Len: -1, Align: 'i', format: fmtNumeric}, "decimal")
	reg(&Type{OID: OIDUUID, Name: "uuid", Len: 16, Align: 'c', format: fmtUUID})
	reg(&Type{OID: OIDPgLSN, Name: "pg_lsn", Len: 8, Align: 'd', ByVal: true, format: fmtLSN})
	reg(&Type{OID: OIDXid8, Name: "xid8", Len: 8, Align: 'd', ByVal: true, format: fmtInt8})
	reg(&Type{OID: OIDJSONB, Name: "jsonb", Len: -1, Align: 'i'})
	reg(&Type{OID: OIDAclitem, Name: "aclitem", Len: 16, Align: 'd'})
	reg(&Type{OID: OIDAnyArray, Name: "anyarray", Len: -1, Align: 'd'})
	reg(&Type{OID: OIDInt2Vector, Name: "int2vector", Len: -1, Align: 'i', Elem: OIDInt2})
	reg(&Type{OID: OIDOidVector, Name: "oidvector", Len: -1, Align: 'i', Elem: OIDOid})

	arr(1000, "_bool", OIDBool, 'i')
	arr(1001, "_bytea", OIDBytea, 'i')
	arr(1002, `_"char"`, OIDChar, 'i')
	arr(1003, "_name", OIDName, 'i')
	arr(1005, "_int2", OIDInt2, 'i')
	arr(1007, "_int4", OIDInt4, 'i')
	arr(1009, "_text", OIDText, 'i')
	arr(1014, "_bpchar", OIDBpchar, 'i')
	arr(1015, "_varchar", OIDVarchar, 'i')
	arr(1016, "_int8", OIDInt8, 'd')
	arr(1021, "_float4", OIDFloat4, 'i')
	arr(1022, "_float8", OIDFloat8, 'd')
	arr(1028, "_oid", OIDOid, 'i')
	arr(1034, "_aclitem", OIDAclitem, 'd')
	arr(1115, "_timestamp", OIDTimestamp, 'd')
	arr(1182, "_date", OIDDate, 'i')
	arr(1183, "_time", OIDTime, 'd')
	arr(1185, "_timestamptz", OIDTimestamptz, 'd')
	arr(1187, "_interval", OIDInterval, 'd')
	arr(1231, "_numeric", OIDNumeric, 'i')
	arr(2951, "_uuid", OIDUUID, 'i')
	arr(3807, "_jsonb", OIDJSONB, 'i')
	arr(199, "_json", OIDJSON, 'i')
}

// Lookup returns a built-in type by OID.
func Lookup(oid uint32) (*Type, bool) {
	t, ok := builtins[oid]
	return t, ok
}

// LookupName resolves a SQL type name as written in a schema ("integer",
// "varchar(40)", "numeric(12,2)", "text[]", "timestamp with time zone").
func LookupName(name string) (*Type, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(n, "_") { // internal array type name, e.g. _int4
		n = n[1:] + "[]"
	}
	isArray := strings.HasSuffix(n, "[]")
	n = strings.TrimSpace(strings.TrimSuffix(n, "[]"))
	if i := strings.IndexByte(n, '('); i >= 0 {
		if j := strings.IndexByte(n[i:], ')'); j >= 0 {
			n = strings.TrimSpace(n[:i] + n[i+j+1:])
		}
	}
	n = strings.Join(strings.Fields(n), " ")
	oid, ok := byName[n]
	if !ok {
		return nil, false
	}
	if !isArray {
		return builtins[oid], true
	}
	for _, t := range builtins {
		if t.Elem == oid && strings.HasPrefix(t.Name, "_") {
			return t, true
		}
	}
	return nil, false
}

// Supported reports whether values of this type can be rendered as text.
func (t *Type) Supported() bool { return t.format != nil || t.Elem != 0 }
