"""Package captured PostgreSQL fixtures into the repository as gzip files."""
import gzip, os, shutil, sys

src, dst = sys.argv[1], sys.argv[2]
out = os.path.join(src, "out")
plan = {
    "raw": os.path.join(out, "raw"),
    "vacuumed": os.path.join(out, "vacuumed"),
    "dropped": os.path.join(out, "dropped"),
}
keys = ["pages.json", "items.json", "catalog.json", "xids.json",
        "expected_orig.jsonl", "expected_live.jsonl", "expected_blobs.jsonl"]

def gz(a, b):
    os.makedirs(os.path.dirname(b), exist_ok=True)
    with open(a, "rb") as f, gzip.GzipFile(b + ".gz", "wb", mtime=0) as g:
        shutil.copyfileobj(f, g)

for name, root in plan.items():
    for dirpath, _, files in os.walk(root):
        for fn in files:
            a = os.path.join(dirpath, fn)
            rel = os.path.relpath(a, root)
            if name == "vacuumed" and fn.endswith(".json"):
                gz(a, os.path.join(dst, "key", "vacuumed_" + fn))
            else:
                gz(a, os.path.join(dst, name, rel))
for k in keys:
    gz(os.path.join(out, k), os.path.join(dst, "key", k))

total = 0
for dirpath, _, files in os.walk(dst):
    for fn in files:
        total += os.path.getsize(os.path.join(dirpath, fn))
print("packed bytes:", total)
