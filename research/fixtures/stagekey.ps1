param([string]$Stage)
# Snapshot the accounts heap + pageinspect key for one stage, without reading table rows.
$bin = "C:\Program Files\PostgreSQL\17\bin"
$w = "C:\Users\user\AppData\Local\Temp\claude\C--Users-user\1fe856cf-0589-45c0-b5ba-6cedfbff9315\scratchpad\pgfix"
$o = "$w\out\stages\$Stage"
New-Item -ItemType Directory -Force $o | Out-Null
$p = @("-h","localhost","-p","55432","-U","postgres","-X","-d","lab","-q","-A","-t")
& "$bin\psql.exe" @p -c "CHECKPOINT"
& "$bin\psql.exe" @p -o "$o\items.json" -c "SELECT json_agg(r ORDER BY blkno, lp) FROM (SELECT b AS blkno, i.lp, i.lp_off, i.lp_flags, i.lp_len, i.t_xmin::text::bigint AS t_xmin, i.t_xmax::text::bigint AS t_xmax, i.t_ctid::text AS t_ctid, i.t_infomask2, i.t_infomask, i.t_hoff FROM generate_series(0, pg_relation_size('accounts')/8192 - 1) b, heap_page_items(get_raw_page('accounts', b)) i) r"
& "$bin\psql.exe" @p -o "$o\pages.json" -c "SELECT json_agg(r ORDER BY blkno) FROM (SELECT b AS blkno, h.lsn::text AS lsn, h.flags, h.lower, h.upper, h.special, h.checksum, page_checksum(get_raw_page('accounts', b), b) AS computed_checksum FROM generate_series(0, pg_relation_size('accounts')/8192 - 1) b, page_header(get_raw_page('accounts', b)) h) r"
$path = & "$bin\psql.exe" @p -c "select pg_relation_filepath('accounts')"
Copy-Item "$w\data\$($path -replace '/','\')" "$o\accounts.heap"
Copy-Item "$w\data\pg_xact\0000" "$o\pg_xact_0000"
$it = Get-Content "$o\items.json" -Raw | ConvertFrom-Json
"{0}: file={1} pages={2} normal={3} redirect={4} dead={5} unused={6} with_xmax={7}" -f $Stage, $path, ((Get-Item "$o\accounts.heap").Length/8192),
  ($it | ? lp_flags -eq 1).Count, ($it | ? lp_flags -eq 2).Count, ($it | ? lp_flags -eq 3).Count, ($it | ? lp_flags -eq 0).Count,
  ($it | ? { $_.lp_flags -eq 1 -and $_.t_xmax -ne 0 }).Count
