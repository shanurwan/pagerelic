# Runbook: dropped table

`DROP TABLE` deletes the table's rows from `pg_class` and `pg_attribute`
(the tuples stay until VACUUM) and unlinks its data files at commit or the
next checkpoint. Recovery needs two things: the **schema**, from those
deleted catalog tuples, and the **pages**, from the old file if something
still holds it or carved off the disk.

## 1. Freeze the evidence

Stop PostgreSQL immediately. Autovacuum on the catalogs removes the deleted
catalog tuples, and new writes reuse the freed disk blocks. Image the disk
with BareRelic (`barerelic image`); don't mount it read-write.

## 2. Recover the schema

```sh
pagerelic relations --datadir /copy/pgdata --db shop --dropped
```

Dropped tables show `STATE deleted` and `FILE missing`, followed by their
columns, e.g. `no int8, customer text, amount numeric, issued date, paid bool`.
Keep that line; it is the `--schema` for the next step.

## 3. Get the pages back

- If the file still exists (a file-level backup, a snapshot, a copy made
  before the checkpoint), use it directly.
- Otherwise carve the disk image:

```sh
pagerelic carve --out carved/ disk.img
# heap pages are grouped by attribute count: a 5-column table -> heap-natts5.pages
```

## 4. Decode

```sh
pagerelic rows --checksums off --remnants \
  --schema "no int8, customer text, amount numeric, issued date, paid bool" \
  --xact /copy/pgdata/pg_xact \
  carved/heap-natts5.pages
```

`--checksums off` is required for carved pages: their block numbers within
the table are unknown, and the checksum depends on them. `carve` records the
checksum-consistent block candidates of each page in `carved/index.jsonl`.

If several dropped tables had the same number of columns, their pages
share a group. Rows that don't decode under a schema are reported with
decode errors, which separates the tables.
