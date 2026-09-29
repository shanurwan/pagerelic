# PageRelic working rules

PageRelic recovers PostgreSQL data from on-disk pages, offline. Read
`docs/architecture.md`, `docs/on-disk-format.md` and
`docs/research/findings.md` first.

- Preserve user edits. Do not commit or push.
- Never write to input files, and never point PageRelic at a production
  cluster's live data directory. Use `testdata/pg17` or a throwaway cluster.
- Format code must be validated against real PostgreSQL output
  (`research/fixtures`), not only hand-built bytes.
- Standard library only. Bound every allocation and loop driven by on-disk
  values, and fuzz every parser.
- Never guess MVCC state; `unknown` is an honest answer.
- Keep validation boundaries in the docs accurate.

Build/test: `go test ./...`, `go vet ./...`, `gofmt -l .`;
`CGO_ENABLED=0 go build ./cmd/pagerelic`. Fuzz: `make fuzz`.
