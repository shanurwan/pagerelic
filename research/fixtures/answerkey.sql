-- Raw-page answer key from PostgreSQL's own pageinspect. Does not set hint bits.
\set ON_ERROR_STOP on
\pset format unaligned
\pset tuples_only on
\pset footer off

\o :outdir/pages.json
SELECT json_agg(r ORDER BY rel, blkno) FROM (
  SELECT rel, b AS blkno, h.lsn::text AS lsn, h.checksum, h.flags, h.lower, h.upper, h.special,
         h.pagesize, h.version, h.prune_xid::text::bigint AS prune_xid,
         page_checksum(get_raw_page(rel, b), b) AS computed_checksum
  FROM (VALUES ('people'), ('blobs')) v(rel),
       generate_series(0, pg_relation_size(v.rel::regclass) / 8192 - 1) b,
       page_header(get_raw_page(rel, b)) h
) r;

\o :outdir/items.json
SELECT json_agg(r ORDER BY rel, blkno, lp) FROM (
  SELECT rel, b AS blkno, i.lp, i.lp_off, i.lp_flags, i.lp_len,
         i.t_xmin::text::bigint AS t_xmin, i.t_xmax::text::bigint AS t_xmax, i.t_field3::text::bigint AS t_field3,
         i.t_ctid::text AS t_ctid, i.t_infomask2, i.t_infomask, i.t_hoff, i.t_bits
  FROM (VALUES ('people'), ('blobs')) v(rel),
       generate_series(0, pg_relation_size(v.rel::regclass) / 8192 - 1) b,
       heap_page_items(get_raw_page(rel, b)) i
) r;

\o :outdir/catalog.json
SELECT json_build_object(
  'server_version_num', current_setting('server_version_num')::int,
  'block_size', current_setting('block_size')::int,
  'database_oid', (SELECT oid FROM pg_database WHERE datname = current_database()),
  'paths', json_build_object(
     'people', pg_relation_filepath('people'), 'people_toast', pg_relation_filepath((SELECT reltoastrelid FROM pg_class WHERE relname='people')),
     'blobs', pg_relation_filepath('blobs'), 'blobs_toast', pg_relation_filepath((SELECT reltoastrelid FROM pg_class WHERE relname='blobs')),
     'pg_class', pg_relation_filepath('pg_class'), 'pg_attribute', pg_relation_filepath('pg_attribute'),
     'pg_type', pg_relation_filepath('pg_type'), 'pg_namespace', pg_relation_filepath('pg_namespace')),
  'relations', (SELECT json_agg(json_build_object('oid', c.oid, 'relname', c.relname, 'relfilenode', c.relfilenode,
                     'reltoastrelid', c.reltoastrelid, 'relkind', c.relkind, 'relnamespace', c.relnamespace) ORDER BY c.oid)
                FROM pg_class c WHERE c.relname IN ('people', 'blobs', 'pg_class', 'pg_attribute', 'pg_type')
                   OR c.oid IN (SELECT reltoastrelid FROM pg_class WHERE relname IN ('people','blobs'))),
  'people_columns', (SELECT json_agg(json_build_object('attnum', attnum, 'attname', attname, 'atttypid', atttypid,
                     'attlen', attlen, 'attalign', attalign, 'attbyval', attbyval, 'atttypmod', atttypmod) ORDER BY attnum)
                     FROM pg_attribute WHERE attrelid = 'people'::regclass AND attnum > 0),
  'pg_attribute_layout', (SELECT json_agg(json_build_object('attnum', attnum, 'attname', attname, 'atttypid', atttypid,
                     'attlen', attlen, 'attalign', attalign) ORDER BY attnum)
                     FROM pg_attribute WHERE attrelid = 'pg_attribute'::regclass AND attnum > 0)
);

\o :outdir/xids.json
SELECT json_agg(json_build_object('xid', x, 'status', pg_xact_status(x::text::xid8)) ORDER BY x) FROM (
  SELECT DISTINCT t AS x FROM (
    SELECT i.t_xmin::text::bigint AS t FROM heap_page_items(get_raw_page('people', 0)) i
    UNION SELECT i.t_xmax::text::bigint FROM heap_page_items(get_raw_page('people', 0)) i
    UNION SELECT i.t_xmin::text::bigint FROM generate_series(0, pg_relation_size('people')/8192 - 1) b, heap_page_items(get_raw_page('people', b)) i
    UNION SELECT i.t_xmax::text::bigint FROM generate_series(0, pg_relation_size('people')/8192 - 1) b, heap_page_items(get_raw_page('people', b)) i
  ) s WHERE t >= 3
) q;
\o
