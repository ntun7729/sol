#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
DEST="$ROOT/android/app/src/main/jni/hev-socks5-tunnel"
REV="9a06bc6e7989da54e3d32ff701ef7a7ce4995d3a"

if [ -d "$DEST/.git" ]; then
    current=$(git -C "$DEST" rev-parse HEAD)
    if [ "$current" = "$REV" ]; then
        git -C "$DEST" submodule update --init --recursive
        exit 0
    fi
    rm -rf "$DEST"
fi

git clone https://github.com/heiher/hev-socks5-tunnel.git "$DEST"
git -C "$DEST" checkout "$REV"
git -C "$DEST" submodule update --init --recursive
