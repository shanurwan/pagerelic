# Research findings

Each finding lists how it was produced so it can be reproduced. Fixtures and
SQL are in `testdata/pg17` and `research/fixtures`. The environment was
PostgreSQL 17.4 (x86-64, Windows build), `initdb --data-checksums -E UTF8
--locale=C`, a throwaway cluster on a private port.

## F1. Deleted rows disappear from line pointers almost immediately

**Question.** After `DELETE`, how long do deleted tuples remain addressable,
and what survives pruning and `VACUUM`?

**Method.** `research/fixtures/stages.sql` with `autovacuum = off`:
table `accounts` (`fillfactor = 100`), 1,000 rows; `DELETE` 200
(`id % 5 = 0`); `UPDATE` 111 (`id % 9 = 0`, 89 of them still present);
insert 5 and `ROLLBACK`. Snapshots, each after `CHECKPOINT`, with
`pageinspect` keys (`research/fixtures/stagekey.ps1`):

- **A**: immediately after the DML, with no reads in between;
- **B**: after one `SELECT count(*)`;
- **C**: after `VACUUM`.

Recovery: `pagerelic rows --remnants` (line pointers plus free-space carving),
each recovered row compared byte-for-byte with PostgreSQL's own output for
the original and updated versions (`TestPruningExperiment`).

**Line pointer states (pageinspect):**

| Stage | normal | redirect | dead | unused | tuples with xmax |
|---|---|---|---|---|---|
| A | 906 | 0 | 188 | 0 | 101 |
| B | 878 | 28 | 188 | 0 | 73 |
| C | 800 | 89 | 0 | 200 | 0 |

**Recovered by PageRelic:**

| Stage | via line pointers | via remnants | deleted rows | pre-update versions | aborted | false positives |
|---|---|---|---|---|---|---|
| A | 906 | 19 | 31 / 200 | 89 / 89 | 5 / 5 | 0 |
| B | 878 | 21 | 31 / 200 | 63 / 89 | 5 / 5 | 0 |
| C | 800 | 37 | 22 / 200 | 10 / 89 | 5 / 5 | 0 |

**Findings.**

1. Even in stage A, with no reads and autovacuum off, **188 of 200 deleted
   tuples had already lost their storage** (LP_DEAD, length 0). The `UPDATE`
   that ran after the `DELETE` scanned the same pages and pruned them
   opportunistically (`heap_page_prune_opt` runs when a page is nearly full).
   The practical rule: **any statement that touches a table after a
   mistaken `DELETE` can destroy the easy recovery path.** Stop writes and
   image first.
2. Pruning compacts the page, but it does not erase the bytes it moves
   away from. Tuple images stay in the gap between `pd_lower` and
   `pd_upper`. Carving recovered 19 deleted rows at stage A that no line
   pointer referenced, and **22 deleted rows even after `VACUUM`**.
3. Carving also finds *stale copies*: older images of rows that were moved or
   updated later. PageRelic removes exact duplicates of referenced tuples and
   labels the rest `superseded`, because an unreferenced tuple can never be a
   row's current version. Without this rule, 9 remnants in stage C would have
   been misreported as live.
4. Rows from rolled-back transactions survive every stage (5 / 5).
5. With strict acceptance (valid header, schema's attribute count, plausible
   xmin, every attribute decoding cleanly) carving produced **no false
   positives** in any stage.

**Limits of this finding.** One table shape, one PostgreSQL version, one
workload. Free-space survival depends on tuple width, fill factor, and how
much new data is later written into the page.

## F2. Autovacuum can clean up before you look

The first fixture (`people`, autovacuum on) was snapshotted about a minute
after its DML. All its pages already carried `PD_ALL_VISIBLE`, which only
VACUUM sets, and no tuple kept an `xmax`. Autovacuum had processed the
table. `log_autovacuum_min_duration` (10 min by default in PostgreSQL 15+)
kept this out of the server log. **Forensic implication:** the absence of
deleted tuples is not evidence that none were deleted; check
`PD_ALL_VISIBLE` and `pg_stat_user_tables`.

## F3. `pageinspect`'s checksum column is not the on-disk checksum

`page_header(get_raw_page(...)).checksum` returned 0 for every page of a
checksum-enabled cluster. The files on disk carried non-zero checksums equal
to `page_checksum(get_raw_page(rel, blk), blk)`. PostgreSQL computes the
checksum on the copy it writes (`PageSetChecksumCopy`), not on the buffer.
Tools and tests must compare disk bytes with `page_checksum()`.

## F4. A checksum table from memory is a latent bug

PageRelic's first checksum implementation used a constant table written
from memory. Two of the 32 constants were wrong. Every synthetic test
would have passed. Comparing against real pages failed on all 15 at once,
and the fix came from `include/server/storage/checksum_impl.h`. Real
fixtures are the only acceptable test oracle for format code.

## F5. pg_attribute can describe itself

Catalog layouts change between major versions, and hard-coding them per
version is brittle. `pg_attribute`'s first three columns are stable, which is
enough to read the rows describing `pg_attribute` itself, derive its full
layout, and verify it by decoding `attnum = 1..n`. This worked unchanged on
PostgreSQL 17. It is the basis for reading any other catalog, and for
recovering the schema of a dropped table from its deleted `pg_attribute`
tuples (`TestDroppedTableRecovery`).

## F6. Checksums leak a page's block number

Because the checksum folds in the block number *after* the page hash,
`BlockSum` can be computed once per carved page and every block number in
a range tested cheaply. For all 28 fixture pages the true block was among
the candidates. Within one 1 GiB segment, a page typically has one or two
candidates. This helps order carved pages and detect mis-assembled ones.

## Open questions

- How do survival rates change with `fillfactor < 100`, wider rows, and HOT vs non-HOT updates?
- Can full-page images in WAL (`pg_wal`) supply older versions of pruned pages?
- Can B-tree leaf pages be used to recover keys of deleted rows after heap pruning?
- Cross-version validation: PostgreSQL 12–16 and 18.
