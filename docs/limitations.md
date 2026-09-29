# Limitations and roadmap

## Validation boundary

Demonstrated against real files from **PostgreSQL 17.4**, x86-64, 8 KiB
pages, data checksums on, UTF-8, `lc_collate = C`:

- page headers and checksums of every fixture page equal `pageinspect`'s;
- every line pointer and tuple header equals `heap_page_items`;
- all 360 rows × 20 columns of `people` equal PostgreSQL's text output
  (int2/4/8, text, varchar, char(n), numeric incl. NaN, bool, date,
  timestamptz, timestamp, float4/8, uuid, text[] with NULLs, int4[], bytea,
  interval, TOASTed text);
- all four TOAST paths (inline pglz, inline LZ4, external plain, external
  pglz/LZ4) reassemble exactly;
- `pg_xact` statuses equal `pg_xact_status()`;
- catalog-derived schemas equal `pg_attribute`, including a dropped table's;
- remnant carving: zero false positives across the three-stage experiment.

Not yet demonstrated:

- PostgreSQL 12–16 and 18 (page and tuple formats are stable since 8.3; the
  catalog bootstrap is version-independent by design but untested elsewhere);
- big-endian platforms, non-default `BLCKSZ`, 32-bit builds;
- real-world corrupted clusters (corruption tests inject damage into real pages);
- terabyte-scale throughput.

## Current software limits

| Area | Limit |
|---|---|
| Access methods | Heap is decoded. B-tree, hash, GiST, SP-GiST, GIN, BRIN and sequence pages are identified and verified, not decoded |
| Types | Rendered: the built-ins above plus json, xml, name, oid, xid, tid, regclass, pg_lsn, `"char"`, int2vector/oidvector and arrays of built-ins. `jsonb`, `inet`/`cidr`, geometric, range, enum, domain and composite types are shown as hex with a note (enum labels need `pg_enum`) |
| Missing columns | Columns added after a tuple was written read as NULL; `attmissingval` defaults are not yet applied |
| MultiXact | Tuples whose xmax is a MultiXact are `unknown` unless hint bits decide them (`pg_multixact` is not read) |
| Sub-transactions | `sub-committed` XIDs are `unknown` (`pg_subtrans` is not read) |
| Encoding | Text is emitted as stored; the database encoding is assumed to be UTF-8 |
| Encryption | Page-level encryption (e.g. TDE forks) is not supported |
| Carving | Pages are grouped by access method and attribute count; tables with the same attribute count share a group until decoded with a schema |
| Raw devices | `carve` reads files and Linux/macOS block devices; on Windows, image the device with BareRelic first |

## Roadmap

1. Cross-version fixture matrix (PostgreSQL 12–18) in CI via containers.
2. WAL reader: full-page images and heap records as a source of older page versions.
3. `jsonb`, `inet`, range and enum decoding; `attmissingval`.
4. B-tree leaf decoding to recover keys of deleted rows.
5. `pg_multixact` and `pg_subtrans` readers.
6. `salvage`: write a repaired relation file with damaged pages replaced, for `pg_upgrade`-free restarts.
7. Benchmarks on multi-GB relations; streaming TOAST index.
