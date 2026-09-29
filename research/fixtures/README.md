# Regenerating the PostgreSQL fixtures

`testdata/pg17` holds real files written by PostgreSQL 17.4 together with
answer keys produced by PostgreSQL itself. Every decoder test compares
against them. This is how they were produced; rerun it to add a version.

## 1. Throwaway cluster

```sh
initdb -D work/data -U postgres --auth=trust -E UTF8 --locale=C --data-checksums
pg_ctl -D work/data -o "-p 55432 -c listen_addresses=localhost" -l work/pg.log start
createdb -h localhost -p 55432 -U postgres lab
```

## 2. Scenario A: types, TOAST, DML (`people`, `blobs`)

```sh
psql -h localhost -p 55432 -U postgres -d lab -f scenario.sql
# then create blobs (below), each statement run on its own
psql ... -v outdir=work/out -f answerkey.sql       # pageinspect answer key: raw pages, BEFORE any SELECT
psql ... -c CHECKPOINT                            # then copy the relation, catalog, pg_xact, pg_control files
psql ... -v outdir=work/out -f expected.sql       # PostgreSQL text output of every row version (sets hint bits)
```

`blobs` covers the four TOAST storage paths:

```sql
CREATE TABLE blobs (id int4, kind text, body text);
ALTER TABLE blobs ALTER COLUMN body SET STORAGE EXTENDED;
INSERT INTO blobs SELECT 1, 'external-compressed-pglz', (SELECT string_agg('aaaaaaaaaaaaaaaa' || md5(g::text), '') FROM generate_series(1,3000) g);
INSERT INTO blobs SELECT 2, 'external-plain', (SELECT string_agg(md5('x'||g::text), '') FROM generate_series(1,300) g);
ALTER TABLE blobs ALTER COLUMN body SET COMPRESSION lz4;
INSERT INTO blobs SELECT 3, 'external-compressed-lz4', (SELECT string_agg('bbbbbbbbbbbbbbbb' || md5(g::text), '') FROM generate_series(1,3000) g);
INSERT INTO blobs SELECT 4, 'inline-lz4', repeat('lz4 inline value ', 300);
CHECKPOINT;
```

## 3. Scenario B: DROP TABLE (`dropped/`)

Create and fill `invoices`, `CHECKPOINT`, copy its file, `DROP TABLE`,
`CHECKPOINT`, then copy `pg_class`, `pg_attribute`, `pg_type`,
`pg_filenode.map` and `pg_xact`.

## 4. Scenario C: pruning experiment (`stages/`)

Restart with `-c autovacuum=off`, run `stages.sql`, then
`stagekey.ps1 A-intact`, `SELECT count(*) FROM accounts`,
`stagekey.ps1 B-pruned`, `VACUUM accounts`, `stagekey.ps1 C-vacuumed`,
and export expected values with the same `json_build_object` pattern as
`expected.sql`.

## 5. Package

```sh
python pack.py work testdata/pg17      # gzip, mtime 0, deterministic
```

Rules:

- capture `pageinspect` keys **before** any `SELECT`: reads set hint bits
  and can prune pages;
- compare on-disk checksums with `page_checksum()`, not
  `page_header().checksum` (findings F3);
- keep fixtures small (the whole set is about 0.7 MB compressed) and never
  include real data.
