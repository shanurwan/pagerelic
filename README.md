# PageRelic

**PostgreSQL page-level recovery and forensic analysis.**

PageRelic reads PostgreSQL's data files directly, page by page, without a
running server. It gets rows back from situations where PostgreSQL itself
can't help: deleted and updated rows, rolled-back inserts, dropped tables,
pages with checksum failures or destroyed headers, and data directories that
only survive as pages scattered across a raw disk. It is also a research
project into how PostgreSQL's storage engine behaves on disk, with every
finding backed by real PostgreSQL files and PostgreSQL's own answer keys.

| Situation | What PageRelic does | Command |
|---|---|---|
| Rows deleted or overwritten by `UPDATE` | reads every tuple version still on the page and classifies it from hint bits and `pg_xact`: live, deleted, updated, aborted, in-progress | `rows` |
| Rows already pruned or `VACUUM`ed | carves tuple remnants out of page free space, rejecting anything that doesn't decode cleanly | `rows --remnants` |
| `DROP TABLE` | rebuilds the schema from the *deleted* `pg_class` and `pg_attribute` tuples, then decodes the table's pages (carved from disk if the file is gone) | `relations --dropped`, `rows` |
| Corrupted pages (bit rot, torn writes, zeroed headers) | verifies every checksum and header, keeps decoding around damage, and grades each row's confidence | `verify`, `inspect`, `rows` |
| No schema available | derives table layouts from the system catalogs on disk, version-independently | `relations`, `rows --table` |
| Data directory lost from the file system | finds PostgreSQL pages on a raw disk or image, groups them by relation, and infers block numbers from checksums | `carve` |
| Out-of-line values | reassembles TOAST chunks, including those of deleted rows, and decompresses pglz and LZ4 | automatic |

> **Validation boundary.** Every decoder is tested against files written by
> a real **PostgreSQL 17.4** (x86-64, 8 KiB pages, data checksums on) and
> compared with PostgreSQL's own `pageinspect` output and text output:
> page headers and checksums on every page, every line pointer and tuple
> header, all 360 rows of a 20-column table across 20 data types, all four
> TOAST storage paths, `pg_xact` status, and catalog-derived schemas.
> Other major versions, big-endian platforms, non-default `BLCKSZ`, and
> real-world corrupted clusters have **not** been validated yet. See
> [limitations](docs/limitations.md).

## Quick start

Work on a **stopped** cluster or, better, a copy or image of it
(`barerelic image`, `pg_basebackup`, a file-system snapshot). PageRelic
never writes to its inputs.

```sh
make build                                   # -> bin/pagerelic

# What is in this data directory?
pagerelic relations --datadir /srv/pgdata                        # databases
pagerelic relations --datadir /srv/pgdata --db shop --dropped    # tables, incl. dropped

# Deleted and pre-update rows of one table, including rows already pruned:
pagerelic rows --datadir /srv/pgdata --db shop --table public.orders \
               --remnants --state deleted,updated,superseded --out orders-deleted.jsonl

# Without catalogs (a lone relation file), give the layout yourself:
pagerelic rows --schema "id int4, customer text, total numeric(10,2), placed timestamptz" \
               --xact /srv/pgdata/pg_xact --toast /srv/pgdata/base/16384/16390 \
               /srv/pgdata/base/16384/16387

# Integrity of a whole cluster, offline:
pagerelic verify /srv/pgdata

# Data directory deleted? Carve pages off the disk image, then decode a group:
pagerelic carve --out carved/ disk.img
pagerelic rows --checksums off --remnants --schema "..." carved/heap-natts5.pages
```

Every row carries its provenance and a verdict:

```json
{"_block":1,"_lp":0,"_offset":456,"_source":"remnant","_state":"deleted","_confidence":"medium",
 "_xmin":767,"_xmax":768,"_ctid":"(1,71)","_evidence":"hint-bits",
 "id":"145","owner":"owner-145-20dfa0","balance":"1051.25","opened":"2021-06-19 03:00:00+00",
 "memo":"memo ppppppppppppppppppppppppppppppppppppppppppppp",
 "_notes":["reconstructed from page free space: no line pointer references it"]}
```

Values are rendered exactly as PostgreSQL's output functions render them
(what `psql` shows with `TimeZone=UTC`, `DateStyle=ISO`,
`IntervalStyle=postgres`), so recovered data can be loaded back with `COPY`.

## Commands

| Command | Purpose |
|---|---|
| `inspect <file>` | page headers, checksum status, page type; `--block N` shows line pointers and tuple headers |
| `verify <files or datadir>` | offline checksum and structure verification of every relation fork |
| `rows` | decode tuples by `--schema` or by `--datadir --db --table`; `--remnants`, `--state`, `--format jsonl\|csv`, `--out` (with a SHA-256 manifest) |
| `relations --datadir` | list databases; with `--db`, list relations, files, TOAST tables and column layouts; `--dropped` |
| `carve <disk-or-image>` | find PostgreSQL pages on raw media and group them by relation |
| `xact` | commit-log status of transaction IDs |
| `version` | build identity |

**Exit codes:** `0` clean · `1` failure · `2` usage · `3` corruption or
undecodable data found · `130` interrupted.

## Research highlights

From the controlled experiment in [docs/research/findings.md](docs/research/findings.md)
(1,000 rows; 200 deleted, 89 updated, 5 rolled back; autovacuum off):

| Stage | Deleted rows recoverable | Pre-update versions recoverable | False positives |
|---|---|---|---|
| Right after the DML | 31 / 200 | 89 / 89 | 0 |
| After one `SELECT` (on-access pruning) | 31 / 200 | 63 / 89 | 0 |
| After `VACUUM` | **22 / 200** (all from free space) | 10 / 89 | 0 |

The most important operational finding: a `DELETE` followed by *any*
statement that touches the page can prune the deleted tuples almost
immediately, even with autovacuum off. **Take the cluster offline or
image it before anything else touches the table.**

## Documentation

- [Architecture](docs/architecture.md)
- [On-disk format reference](docs/on-disk-format.md): what PageRelic decodes, with source references
- [Research findings](docs/research/findings.md): experiments, results, method
- [Operations](docs/operations.md): safe usage, output formats, exit codes
- [Limitations and roadmap](docs/limitations.md)
- Runbooks: [deleted rows](docs/runbooks/deleted-rows.md) ·
  [dropped table](docs/runbooks/dropped-table.md) ·
  [corrupted pages](docs/runbooks/corrupted-pages.md) ·
  [lost data directory](docs/runbooks/lost-data-directory.md)

PageRelic pairs with [BareRelic](https://github.com/shanurwan/BareRelic):
BareRelic images failing media and recovers files; PageRelic turns
PostgreSQL pages into rows.

## Development

```sh
make check    # gofmt, vet, tests (real PostgreSQL 17 fixtures in testdata/pg17)
make race     # needs cgo
make lint     # staticcheck
make vuln     # govulncheck
make fuzz     # every parser of on-disk data
```

Fixtures are regenerated from SQL in [research/fixtures](research/fixtures);
see [CONTRIBUTING.md](CONTRIBUTING.md).
