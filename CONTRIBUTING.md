# Contributing

## Ground rules

1. **PostgreSQL is the oracle.** New decoders need a real fixture, generated
   by PostgreSQL and packed into `testdata/pg<version>`, and a test comparing
   against PostgreSQL's own output (`pageinspect`, output functions). A
   decoder tested only against hand-built bytes isn't done (see findings F4).
2. **Never write to inputs.** Open every input with `os.Open`. Output is
   created with `O_EXCL`.
3. **Treat every byte as hostile.** Bounds-check before slicing, never
   allocate from a declared size without a cap, and add or extend a fuzz
   target for each parser.
4. **Don't guess MVCC state.** When hint bits and `pg_xact` can't decide,
   report `unknown`.
5. **Standard library only.**

## Workflow

```sh
make check      # gofmt, vet, tests
make race lint vuln
make fuzz       # before touching any parser
```

Record new experimental results in `docs/research/findings.md` with the
method used to produce them, and update `CHANGELOG.md`.

## Adding a PostgreSQL version

Follow `research/fixtures/README.md` with that version's binaries, pack the
result into `testdata/pg<N>`, and parameterise the golden tests over the
fixture directories.
