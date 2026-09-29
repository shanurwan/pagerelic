# Operations

## Before running PageRelic

1. **Stop further damage.** Stop the application or revoke write access. If
   rows were deleted by mistake, every later statement that touches the table
   can prune them (docs/research/findings.md, F1).
2. **Work on a copy.** Either stop PostgreSQL (`pg_ctl stop -m fast`) and copy
   the data directory, take a file-system snapshot, or image the disk with
   BareRelic. Reading files from a *running* cluster can show torn pages
   (writes in flight) and misses changes still in shared buffers.
3. **Keep `pg_xact`.** Without the commit log, tuples whose hint bits were
   never set are reported as `unknown`.

## Output

`rows` writes one record per tuple version:

| Field | Meaning |
|---|---|
| `_block`, `_lp`, `_offset` | location (`_lp` 0 for remnants) |
| `_source` | `line-pointer` or `remnant` |
| `_state` | `live`, `deleted`, `updated`, `aborted`, `superseded`, `in-progress`, `deleting`, `unknown` |
| `_confidence` | `high`, `medium`, `low` |
| `_xmin`, `_xmax`, `_ctid`, `_evidence` | MVCC header and how the state was decided (`hint-bits`, `pg_xact`, `frozen`) |
| columns | PostgreSQL text output; `null` for NULL (CSV: `\N`) |
| `_notes` | per-row caveats and per-column decode errors |

With `--out FILE`, PageRelic refuses to overwrite an existing file and
writes `FILE.manifest.json`: tool build, command line, SHA-256 of every input
and of the output, the column layout, and the scan summary.

Loading recovered rows back:

```sh
pagerelic rows ... --state deleted --format csv --out deleted.csv
# drop the _-prefixed provenance columns, then:
psql -c "\copy recovered FROM 'deleted-data.csv' WITH (FORMAT csv, HEADER, NULL '\N')"
```

## Exit codes

| Code | Meaning |
|---|---|
| 0 | completed; no corruption or undecodable data |
| 1 | failed (unreadable input, bootstrap failure, refused overwrite) |
| 2 | usage error |
| 3 | completed, but checksum failures, invalid pages, or undecodable values were found |
| 130 | interrupted |

## Performance and memory

- `rows` streams pages. In testing on this machine, a single page took about
  0.4 ms on average with remnant carving enabled; throughput on large
  relations has not been benchmarked yet.
- The TOAST index keeps all chunks of the TOAST relation in memory; budget
  roughly the TOAST relation's size.
- `carve` reads the source in 8 MiB windows. Block-number inference tests
  up to `--max-block` numbers per page that has a checksum.

## Testing note (Windows)

Go's fuzzing engine reaches very low throughput on Windows for targets whose
inputs are page-sized (8 KiB+), while the same code runs about 0.4 ms per
input outside the fuzzer. CI fuzzes on Linux.
