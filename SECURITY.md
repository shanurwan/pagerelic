# Security policy

PageRelic parses database files that may be corrupted or deliberately
crafted, and those files usually contain sensitive data. Treat these as
security issues:

- any write to an input file or directory;
- overwriting existing output;
- a panic, unbounded memory or CPU use while parsing;
- output that misstates where a row came from, or its MVCC state.

## Reporting

Report privately through GitHub **Security → Report a vulnerability** on
<https://github.com/shanurwan/pagerelic>, not in a public issue. Include
`pagerelic version --json`, the command line, and if possible a minimal
*synthetic* file that reproduces the problem. **Never attach real database
files**: they contain personal data.

## Handling recovered data

Recovered rows are live data, including rows someone deliberately deleted.
Store output with the same controls as the database itself, and delete it
when the recovery is complete.
