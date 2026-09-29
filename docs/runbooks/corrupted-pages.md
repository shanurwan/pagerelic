# Runbook: checksum failures and corrupted pages

**Symptoms:** `ERROR: invalid page in block N of relation base/…`,
`WARNING: page verification failed, calculated checksum X but expected Y`,
crashes when reading a table, or `pg_checksums --check` failures.

## 1. Find all damage, offline

```sh
pg_ctl stop -m fast
pagerelic verify /srv/pgdata            # every fork of every relation, plus global/
pagerelic verify --json /srv/pgdata > verify.json
```

Exit code 3 means damage was found. Each failure names the file, the block,
and the problem: header rule violated, checksum mismatch, or a torn file
size.

## 2. Look at a damaged page

```sh
pagerelic inspect --block 1234 /srv/pgdata/base/16384/16390
```

- **Checksum mismatch, structure intact:** often a single-bit error or a torn
  write. The line pointers are still shown and usually most tuples are fine.
- **Header problems** (`pd_lower > pd_upper`, bad version): the line pointers
  can't be trusted. Recover with remnant carving, which works without them.
- **All zeros:** the page never reached disk (torn extension or a lost write).

## 3. Salvage the rows

```sh
pagerelic rows --datadir /srv/pgdata --db shop --table public.orders \
  --remnants --out orders-salvage.jsonl
```

Rows from pages with checksum failures are kept at `_confidence: low` with a
note. Rows from pages whose header is destroyed are carved from the whole
page body.

## 4. Repair the cluster

PageRelic doesn't write to the cluster. Typical paths after salvaging:

- restore from backup, then re-insert salvaged rows newer than the backup;
- or, on a copy, `SET zero_damaged_pages = on` and `VACUUM FULL` the table
  (this discards the damaged pages), then re-insert the salvaged rows from
  those pages.

Also find the cause: `dmesg`, SMART data, storage controller logs, and
whether `fsync` or write caching was misconfigured.
