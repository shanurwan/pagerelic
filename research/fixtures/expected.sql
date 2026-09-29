-- Expected decoded values, in PostgreSQL text output, for every tuple version
-- ever written to people. Run AFTER the raw snapshot (SELECT sets hint bits).
\set ON_ERROR_STOP on
SET TimeZone = 'UTC';
SET DateStyle = 'ISO, YMD';
SET IntervalStyle = 'postgres';
SET extra_float_digits = 1;
\pset format unaligned
\pset tuples_only on
\pset footer off

-- Re-generate the original inserted rows (the scenario is deterministic).
CREATE TEMP TABLE people_orig (LIKE people);
INSERT INTO people_orig
SELECT i, (i % 300)::int2, i::int8 * 1000000007, 'person ' || i || ' ' || substr(md5(i::text), 1, 8),
       'user' || i || '@example.org',
       CASE WHEN i % 5 = 0 THEN NULL ELSE lpad((i % 97)::text, 3, 'x') END,
       round((i * 13.37)::numeric, 2) * CASE WHEN i % 4 = 0 THEN -1 ELSE 1 END,
       CASE i % 6 WHEN 0 THEN NULL WHEN 1 THEN 0.000123 WHEN 2 THEN 123456789.123456789
                  WHEN 3 THEN -42 WHEN 4 THEN 'NaN'::numeric ELSE (i::numeric / 7) END,
       i % 2 = 0, DATE '1990-01-01' + i * 11,
       TIMESTAMPTZ '2024-03-01 08:00:00+00' + i * INTERVAL '37 minutes',
       TIMESTAMP '1999-12-31 23:59:59.5' + i * INTERVAL '1 second',
       i / 3.0, (i / 7.0)::float4, md5('uid' || i)::uuid,
       CASE WHEN i % 3 = 0 THEN ARRAY['alpha', 'beta' || i, NULL] ELSE ARRAY['solo'] END,
       ARRAY[i, i * 2, i * 3], decode(substr(md5(i::text), 1, 8), 'hex'),
       make_interval(0, i % 12, 0, i % 28, i % 24, i % 60, 0.25),
       CASE WHEN i = 7 THEN repeat('compressible toast value ', 400)
            WHEN i = 8 THEN (SELECT string_agg(md5(i::text || g::text), '') FROM generate_series(1, 400) g)
            WHEN i = 9 THEN repeat('external compressed ' || i, 3000)
            ELSE 'short note ' || i END
FROM generate_series(1, 400) AS i;

\o :outdir/expected_orig.jsonl
SELECT json_build_object('id', id::text, 'small', small::text, 'big', big::text, 'name', name, 'email', email::text,
  'code', code::text, 'balance', balance::text, 'ratio', ratio::text, 'active', active::text, 'born', born::text,
  'created', created::text, 'seen', seen::text, 'score', score::text, 'weight', weight::text, 'uid', uid::text,
  'tags', tags::text, 'nums', nums::text, 'blob', blob::text, 'span', span::text, 'note', note) FROM people_orig ORDER BY id;
\o :outdir/expected_live.jsonl
SELECT json_build_object('id', id::text, 'small', small::text, 'big', big::text, 'name', name, 'email', email::text,
  'code', code::text, 'balance', balance::text, 'ratio', ratio::text, 'active', active::text, 'born', born::text,
  'created', created::text, 'seen', seen::text, 'score', score::text, 'weight', weight::text, 'uid', uid::text,
  'tags', tags::text, 'nums', nums::text, 'blob', blob::text, 'span', span::text, 'note', note) FROM people ORDER BY id;
\o :outdir/expected_blobs.jsonl
SELECT json_build_object('id', id::text, 'kind', kind, 'body', body) FROM blobs ORDER BY id;
\o
