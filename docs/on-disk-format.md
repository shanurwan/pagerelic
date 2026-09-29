# PostgreSQL on-disk format: what PageRelic decodes

A reference to the structures PageRelic reads, with the PostgreSQL source
files that define them. All multi-byte integers are little-endian on the
validated platform (x86-64). Offsets are in bytes.

## Relation files

A relation fork is stored as `base/<db-oid>/<relfilenode>[_fsm|_vm|_init][.N]`.
Each segment `.N` holds up to `RELSEG_SIZE` blocks (1 GiB with 8 KiB pages).
The **absolute block number** of a page, used by the checksum, is
`N × 131072 + block-within-segment`.

## Page layout (`src/include/storage/bufpage.h`)

```text
0      PageHeaderData (24 bytes)
24     ItemIdData[]           line pointers, 4 bytes each, growing up to pd_lower
...    free space             pd_lower .. pd_upper
pd_upper  tuples              growing down from pd_special
pd_special special space      access-method specific (0 bytes for heap)
```

| Offset | Field | Notes |
|---|---|---|
| 0 | `pd_lsn` | two uint32: high then low half of the WAL position |
| 8 | `pd_checksum` | uint16; 0 when checksums are disabled |
| 10 | `pd_flags` | 0x1 HAS_FREE_LINES, 0x2 PAGE_FULL, 0x4 ALL_VISIBLE |
| 12 | `pd_lower` | end of the line pointer array |
| 14 | `pd_upper` | start of tuple space; 0 means an uninitialised ("new") page |
| 16 | `pd_special` | start of special space |
| 18 | `pd_pagesize_version` | page size \| layout version (4 since 8.3) |
| 20 | `pd_prune_xid` | oldest XID that might be prunable |

PageRelic applies PostgreSQL's own header sanity rules
(`PageIsVerifiedExtended`): only defined flag bits,
`24 ≤ pd_lower ≤ pd_upper ≤ pd_special ≤ BLCKSZ`, and `pd_special` MAXALIGNed.

**Line pointer** (`itemid.h`): `lp_off:15 | lp_flags:2 | lp_len:15`.
Flags: 0 unused, 1 normal, 2 redirect (HOT chain root; `lp_off` is the
target item), 3 dead (with or without storage).

**Special space identifies the access method:** heap 0 bytes; B-tree 16 bytes
(`BTPageOpaqueData`); hash, GiST and SP-GiST 16 bytes ending in page IDs
0xFF80 / 0xFF81 / 0xFF82; GIN 8 bytes; BRIN 8 bytes ending 0xF091–0xF093;
sequence 8 bytes with magic 0x1717.

## Data checksums (`src/include/storage/checksum_impl.h`)

The page is viewed as `uint32 data[BLCKSZ/128][32]`. 32 lanes start from
fixed offsets and absorb one word each per row with
`tmp = sum ^ value; sum = tmp * 16777619 ^ (tmp >> 17)`, followed by two
rounds of zeroes. The lanes are XOR-folded, the result is XORed with the
absolute block number, and reduced to `(x % 65535) + 1`. `pd_checksum`
counts as zero during the computation.

Two consequences PageRelic uses:

1. The block-independent part (`BlockSum`) is computed once, so every block
   number consistent with a carved page's stored checksum can be listed
   cheaply. For our fixture pages the true block was always among the
   candidates (`page.CandidateBlocks`).
2. PostgreSQL stamps the checksum on the copy it writes out. Shared buffers,
   and therefore `pageinspect`'s `page_header()`, show a stale `pd_checksum`.
   Compare on-disk checksums with `page_checksum()`, not with
   `page_header().checksum`.

## Heap tuples (`src/include/access/htup_details.h`)

| Offset | Field |
|---|---|
| 0 | `t_xmin` inserting XID |
| 4 | `t_xmax` deleting or locking XID |
| 8 | `t_cid` / `t_xvac` |
| 12 | `t_ctid` (block hi, block lo, offset: 6 bytes) |
| 18 | `t_infomask2`: attribute count (low 11 bits), HOT flags |
| 20 | `t_infomask` |
| 22 | `t_hoff` offset of the data, MAXALIGNed |
| 23 | `t_bits` null bitmap (one bit per attribute; 1 = not null) |

Key `t_infomask` bits: HASNULL 0x0001, HASVARWIDTH 0x0002, HASEXTERNAL 0x0004,
XMAX_KEYSHR_LOCK 0x0010, XMAX_EXCL_LOCK 0x0040, XMAX_LOCK_ONLY 0x0080,
XMIN_COMMITTED 0x0100, XMIN_INVALID 0x0200 (both = frozen),
XMAX_COMMITTED 0x0400, XMAX_INVALID 0x0800, XMAX_IS_MULTI 0x1000, UPDATED 0x2000.

### Attribute layout (`heap_deform_tuple`)

For each attribute in `attnum` order, dropped columns included:

- null per bitmap → no bytes;
- fixed length (`attlen > 0`) → align to `attalign` (c/s/i/d = 1/2/4/8), read `attlen` bytes;
- varlena (`attlen = -1`) → if the byte at the current offset is 0 it is padding: align to `attalign`;
  a non-zero byte is a 1-byte header and is *not* aligned;
- attributes beyond the tuple's own count were added later (`ALTER TABLE … ADD COLUMN`) and read as NULL or their stored default.

## Varlena (`src/include/varatt.h`)

| First byte | Meaning |
|---|---|
| `xxxxxxx1` (≠ 0x01) | 1-byte header, length = byte >> 1 (≤ 126 bytes) |
| `0x01` then tag 18 | on-disk TOAST pointer: `va_rawsize`, `va_extinfo` (size \| method << 30), `va_valueid`, `va_toastrelid` |
| `xxxxxx00` | 4-byte header, length = uint32 >> 2 |
| `xxxxxx10` | 4-byte header, compressed inline; next uint32 = raw size \| method << 30 |

Compression methods: 0 pglz (`common/pg_lzcompress.c`), 1 LZ4 (block format).

**TOAST** (`access/detoast.c`): a TOAST relation holds rows
`(chunk_id oid, chunk_seq int4, chunk_data bytea)`. An external value is the
concatenation of its chunks in `chunk_seq` order. If `extsize < rawsize − 4`,
the reassembled bytes start with the 4-byte compression header.

## Type encodings used by the decoders

| Type | Encoding |
|---|---|
| `numeric` | uint16 header: short form (0x8000: sign 0x2000, dscale bits 7–12, 7-bit signed weight) or long form (sign 0xC000 mask + 14-bit dscale, int16 weight); base-10000 int16 digits; specials 0xC000 NaN, 0xD000 +Inf, 0xF000 −Inf |
| `date` | int32 days since 2000-01-01 |
| `timestamp[tz]` | int64 µs since 2000-01-01 00:00 UTC; ±MaxInt64 = ±infinity |
| `interval` | int64 µs, int32 days, int32 months |
| arrays | `ndim, dataoffset, elemtype, dims[], lbounds[]`, optional null bitmap, elements aligned relative to the 4-byte-header form |
| `uuid` | 16 bytes; `name` 64 bytes NUL-padded; `bool` 1 byte |

## Commit log (`src/backend/access/transam/clog.c`)

`pg_xact/XXXX` segments of 32 pages hold two bits per XID:
0 in progress, 1 committed, 2 aborted, 3 sub-committed. XID `x` lives in
segment `x / 1048576`, byte `(x mod 1048576) / 4`, bits `(x mod 4) × 2`.

## Catalog bootstrap

`pg_class`, `pg_attribute`, `pg_type` and `pg_proc` are *mapped*: their
`relfilenode` is 0 in `pg_class` and the real file number is in
`base/<db>/pg_filenode.map` (`magic 0x592717, n, {oid, filenode}[64], crc32c`;
62 entries before PostgreSQL 16). Shared catalogs such as `pg_database` use
`global/pg_filenode.map`.

`pg_attribute`'s own layout differs between versions, so PageRelic
**derives it from the rows that describe `pg_attribute` itself**. Their first
three columns (`attrelid oid`, `attname name`, `atttypid oid`) are the same in
every supported version. System attributes (`ctid`, `xmin`, …) are excluded
by name. The derived layout is accepted only if decoding with it yields
`attnum = 1..n` in order, with the same `attlen` and `attalign`: a
self-consistency check that fails loudly instead of misreading a catalog.
