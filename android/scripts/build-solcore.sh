#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
NDK=${1:-${ANDROID_NDK_HOME:-}}
if [ -z "$NDK" ]; then
    echo "usage: $0 /path/to/android-ndk" >&2
    exit 2
fi

TOOLCHAIN="$NDK/toolchains/llvm/prebuilt/linux-x86_64/bin"
CC="$TOOLCHAIN/aarch64-linux-android24-clang"
if [ ! -x "$CC" ]; then
    echo "Android NDK clang not found at $CC" >&2
    exit 2
fi

OUT="$ROOT/android/app/src/main/jniLibs/arm64-v8a"
mkdir -p "$OUT"

cd "$ROOT"
go mod tidy
CGO_ENABLED=1 \
GOOS=android \
GOARCH=arm64 \
CC="$CC" \
go build -trimpath -buildmode=c-shared \
    -ldflags='-s -w' \
    -o "$OUT/libsolcore.so" \
    ./android/core

rm -f "$OUT/libsolcore.h"
ls -lh "$OUT/libsolcore.so"
