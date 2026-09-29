# Runbook: rows deleted or overwritten by mistake

**Goal:** get back rows removed by `DELETE`, or the old values replaced by
`UPDATE`, when there is no usable backup or PITR.

## Act immediately

1. Stop writes to the table: stop the application, or
   `REVOKE INSERT, UPDATE, DELETE` from its role. **Don't run `VACUUM`**,
   and avoid queries on the table: any statement that touches its pages
   can prune the deleted tuples (findings F1).
2. Disable autovacuum for the table if the cluster must stay up:
   `ALTER TABLE t SET (autovacuum_enabled = off);` (this is itself a write,
   but only to the catalog).
3. Take a copy: `pg_ctl stop -m fast` and copy the data directory, or a
   file-system snapshot, or `barerelic image` of the disk.

## Recover

```sh
# Which file is the table, and what is its layout?
pagerelic relations --datadir /copy/pgdata --db shop

# Everything that isn't the current version of a row:
pagerelic rows --datadir /copy/pgdata --db shop --table public.orders \
  --remnants --state deleted,updated,superseded,aborted \
  --out orders-recovered.jsonl
```

Read the result:

| `_state` | Meaning |
|---|---|
| `deleted` | removed by a committed `DELETE` |
| `updated` | old version replaced by a committed `UPDATE` (the new version is `live`) |
| `superseded` | an unreferenced older image of a row that later changed or disappeared |
| `aborted` | inserted by a transaction that rolled back |

Rows with `_source: remnant` came from page free space. Their columns decoded
cleanly, but check a sample against application knowledge before restoring
them.

## Restore

Filter the rows you need (e.g. with `jq`), convert to CSV, and load them
into a *new* table. Then compare with the live table before merging, using
`INSERT … SELECT … WHERE NOT EXISTS`.
