#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
NDK=${1:-${ANDROID_NDK_HOME:-}}
if [ -z "$NDK" ]; then
    echo "usage: $0 /path/to/android-ndk" >&2
    exit 2
fi

TOOLCHAIN="$NDK/toolchains/llvm/prebuilt/linux-x86_64/bin"

build_one() {
    ABI=$1
    GOARCH_VALUE=$2
    CC_NAME=$3

    CC="$TOOLCHAIN/$CC_NAME"
    if [ ! -x "$CC" ]; then
        echo "Android NDK clang not found at $CC" >&2
        exit 2
    fi

    OUT="$ROOT/android/app/src/main/jniLibs/$ABI"
    mkdir -p "$OUT"

    CGO_ENABLED=1 \
    GOOS=android \
    GOARCH="$GOARCH_VALUE" \
    CC="$CC" \
    go build -trimpath -buildmode=c-shared \
        -ldflags='-s -w' \
        -o "$OUT/libsolcore.so" \
        ./android/core

    rm -f "$OUT/libsolcore.h"
    ls -lh "$OUT/libsolcore.so"
}

cd "$ROOT"
go mod tidy
build_one arm64-v8a arm64 aarch64-linux-android24-clang
build_one x86_64 amd64 x86_64-linux-android24-clang
