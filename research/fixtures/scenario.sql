-- PageRelic fixture scenario: deterministic data exercising the heap format.
\set ON_ERROR_STOP on
CREATE EXTENSION IF NOT EXISTS pageinspect;

CREATE TABLE people (
  id       int4 PRIMARY KEY,
  small    int2,
  big      int8,
  name     text,
  email    varchar(100),
  code     char(4),
  balance  numeric(12,2),
  ratio    numeric,
  active   bool,
  born     date,
  created  timestamptz,
  seen     timestamp,
  score    float8,
  weight   float4,
  uid      uuid,
  tags     text[],
  nums     int4[],
  blob     bytea,
  span     interval,
  note     text
);
ALTER TABLE people ALTER COLUMN note SET STORAGE EXTENDED;

INSERT INTO people
SELECT i,
       (i % 300)::int2,
       i::int8 * 1000000007,
       'person ' || i || ' ' || substr(md5(i::text), 1, 8),
       'user' || i || '@example.org',
       CASE WHEN i % 5 = 0 THEN NULL ELSE lpad((i % 97)::text, 3, 'x') END,
       round((i * 13.37)::numeric, 2) * CASE WHEN i % 4 = 0 THEN -1 ELSE 1 END,
       CASE i % 6 WHEN 0 THEN NULL WHEN 1 THEN 0.000123 WHEN 2 THEN 123456789.123456789
                  WHEN 3 THEN -42 WHEN 4 THEN 'NaN'::numeric ELSE (i::numeric / 7) END,
       i % 2 = 0,
       DATE '1990-01-01' + i * 11,
       TIMESTAMPTZ '2024-03-01 08:00:00+00' + i * INTERVAL '37 minutes',
       TIMESTAMP '1999-12-31 23:59:59.5' + i * INTERVAL '1 second',
       i / 3.0,
       (i / 7.0)::float4,
       md5('uid' || i)::uuid,
       CASE WHEN i % 3 = 0 THEN ARRAY['alpha', 'beta' || i, NULL] ELSE ARRAY['solo'] END,
       ARRAY[i, i * 2, i * 3],
       decode(substr(md5(i::text), 1, 8), 'hex'),
       make_interval(0, i % 12, 0, i % 28, i % 24, i % 60, 0.25),
       CASE
         WHEN i = 7  THEN repeat('compressible toast value ', 400)           -- inline compressed
         WHEN i = 8  THEN (SELECT string_agg(md5(i::text || g::text), '') FROM generate_series(1, 400) g) -- external, incompressible
         WHEN i = 9  THEN repeat('external compressed ' || i, 3000)          -- external + compressed
         ELSE 'short note ' || i
       END
FROM generate_series(1, 400) AS i;

CHECKPOINT;
-- Committed deletes and updates, plus a rolled-back insert.
DELETE FROM people WHERE id % 10 = 0;
UPDATE people SET balance = balance + 1, name = name || ' (updated)' WHERE id % 7 = 0;
BEGIN;
INSERT INTO people (id, name, note) SELECT 1000 + g, 'ghost ' || g, 'rolled back' FROM generate_series(1, 5) g;
ROLLBACK;
CHECKPOINT;
