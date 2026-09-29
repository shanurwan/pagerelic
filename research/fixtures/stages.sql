-- Controlled pruning experiment. Run with autovacuum=off.
\set ON_ERROR_STOP on
CREATE TABLE accounts (id int4, owner text, balance numeric(12,2), opened timestamptz, memo text) WITH (fillfactor = 100);
INSERT INTO accounts
SELECT i, 'owner-' || i || '-' || substr(md5('o' || i), 1, 6), (i * 7.25)::numeric(12,2),
       TIMESTAMPTZ '2021-06-01 00:00:00+00' + i * INTERVAL '3 hours', 'memo ' || repeat(chr(97 + i % 26), 20 + i % 30)
FROM generate_series(1, 1000) i;
CHECKPOINT;
DELETE FROM accounts WHERE id % 5 = 0;
UPDATE accounts SET balance = balance + 100 WHERE id % 9 = 0;
BEGIN;
INSERT INTO accounts SELECT 5000 + g, 'ghost-' || g, 0, now(), 'rolled back' FROM generate_series(1, 5) g;
ROLLBACK;
CHECKPOINT;
