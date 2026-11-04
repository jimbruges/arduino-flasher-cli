#!/bin/sh
# SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
# SPDX-License-Identifier: GPL-3.0-or-later
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/../.." && pwd)
work_dir="$repo_dir/build/qdl-digest-source"
mkdir -p "$work_dir"
archive="$work_dir/source.tar.gz"
if [ ! -f "$archive" ]; then
    curl --fail --location --output "$archive" https://api.github.com/repos/linux-msm/qdl/tarball/v2.4
fi
python3 - "$archive" <<'PY'
import hashlib, sys
expected = '7c6d397080d519857e3e787847c8fc108dc31d3ccefbe407706bcb8a0fe604b5'
with open(sys.argv[1], 'rb') as source:
    if hashlib.sha256(source.read()).hexdigest() != expected:
        sys.exit('QDL source checksum mismatch')
PY
# Extract a clean source tree on each build, retaining the verified download.
mkdir -p "$work_dir/src"
tar -xzf "$archive" -C "$work_dir/src" --strip-components=1
patch -d "$work_dir/src" -p1 < "$script_dir/storage-digest.patch"
python3 "$script_dir/test-parser.py" "$work_dir/src/firehose.c"
make -C "$work_dir/src" clean
make -C "$work_dir/src" qdl
cp "$work_dir/src/qdl" "$repo_dir/build/qdl-digest"
printf '%s\n' "Built $repo_dir/build/qdl-digest"
