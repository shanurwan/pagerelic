# Runbook: data directory deleted or file system destroyed

When `rm -rf $PGDATA`, a reformat, or file-system damage has removed the
files, the pages themselves often remain on the medium.

## 1. Image the medium

Follow BareRelic's runbooks: stop using the disk, then
`barerelic image --out disk.img /dev/sdX`. Work only on the image.

## 2. Carve PostgreSQL pages

```sh
pagerelic carve --out carved/ disk.img
cat carved/index.jsonl | head       # offset, LSN, kind, tuples, block candidates, group
```

Pages are accepted only if they pass PostgreSQL's header rules, their line
pointers are consistent, and (for checksum-enabled clusters) at least one
block number reproduces their checksum.

Groups: `heap-nattsN.pages` for table pages with N columns, plus `btree`,
`gin` and so on for index pages, which are identified but not decoded.

## 3. Recover schemas

If catalog pages survived, they appear in the heap groups too: `pg_class`
and `pg_attribute` have distinctive attribute counts, 33 and 26 on
PostgreSQL 17 (check with `inspect`). Without them, use the application's
DDL or migrations for `--schema`.

## 4. Decode

```sh
pagerelic rows --checksums off --remnants --schema "..." carved/heap-natts7.pages
```

If `pg_xact` could not be recovered, add `--state all` and treat `unknown`
as "state undetermined". Hint bits already settle most old rows.
