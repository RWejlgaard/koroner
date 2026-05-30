#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/config/crd/bases"
DST="$ROOT/charts/koroner/templates/crds.yaml"

python3 - "$SRC" "$DST" <<'PY'
import os, sys, glob, re

src_dir, dst = sys.argv[1], sys.argv[2]
files = sorted(glob.glob(os.path.join(src_dir, "*.yaml")))

out = ["{{- if .Values.crds.install -}}\n"]
for i, f in enumerate(files):
    with open(f) as fh:
        text = fh.read().rstrip() + "\n"
    # Inject helm.sh/resource-policy: keep into metadata.annotations
    text = re.sub(
        r"(\nmetadata:\n  annotations:\n)",
        r'\1    helm.sh/resource-policy: keep\n',
        text,
        count=1,
    )
    out.append(text)
    out.append("---\n")
# Trim trailing separator
if out[-1] == "---\n":
    out.pop()
out.append("{{- end }}\n")

with open(dst, "w") as fh:
    fh.write("".join(out))
PY

echo "Synced CRDs to $DST"
