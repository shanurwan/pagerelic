# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
the project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Page layer: header parsing with PostgreSQL's sanity rules, line pointers,
  special-space access-method detection, the data checksum algorithm, and
  checksum-based block-number inference.
- Heap tuples: header decoding, infomask flags, MVCC classification from
  hint bits and `pg_xact` (live, deleted, updated, aborted, in-progress,
  deleting, superseded, unknown).
- Value decoding with PostgreSQL text output for 30+ built-in types and
  arrays; varlena headers; inline and external TOAST with pglz and LZ4.
- Remnant carving of tuples from page free space, with stale-copy removal.
- Catalog bootstrap from on-disk files: `pg_filenode.map` (CRC-checked),
  self-derived `pg_attribute` layout, `pg_class`, `pg_namespace`,
  `pg_database`; schema recovery for dropped tables.
- Raw-media page carving with grouping by relation shape.
- CLI: `inspect`, `verify`, `rows`, `relations`, `carve`, `xact`, `version`;
  JSONL/CSV output with SHA-256 manifests.
- Real PostgreSQL 17.4 fixtures with `pageinspect` answer keys, a
  three-stage pruning experiment, and fuzz targets for every parser.
