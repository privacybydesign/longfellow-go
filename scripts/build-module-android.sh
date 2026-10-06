#!/usr/bin/env bash
# Cross-compiles the Go module for android/arm64 and proves it links.
#
# irmago #724 Phase 0 step 4, the half that actually matters to us: the C++
# library cross-building is necessary but not sufficient, because the link only
# happens when cgo brings our binding, the arm64 OpenSSL, zstd and the C++
# runtime together.
#
#   docker run --rm \
#     -v D:\Yivi\longfellow-go:/work/longfellow-go \
#     -w /work/longfellow-go longfellow-android build-module-android.sh
#
# NO DEVICE IS REQUIRED. This produces artefacts and proves they link; running
# them on a phone is a separate exercise.
set -euo pipefail

PREFIX="${PREFIX:-/src/longfellow-zk/install-android-${ANDROID_ABI:-arm64-v8a}}"
API="${ANDROID_API:-24}"
: "${ANDROID_NDK_ROOT:?set ANDROID_NDK_ROOT}"
TC="${ANDROID_NDK_ROOT}/toolchains/llvm/prebuilt/linux-x86_64/bin"
OUT="${OUT:-/tmp/longfellow-android.test}"

test -f "${PREFIX}/lib/libmdoc_static.a" || {
  echo "MISSING ${PREFIX}/lib/libmdoc_static.a -- run build-longfellow-android.sh first"; exit 1; }

export GOOS=android GOARCH=arm64 CGO_ENABLED=1
export CC="${TC}/aarch64-linux-android${API}-clang"
export CXX="${TC}/aarch64-linux-android${API}-clang++"
export CGO_CFLAGS="-I${PREFIX}/include"
export CGO_LDFLAGS="-L${PREFIX}/lib"

echo "==> GOOS=${GOOS} GOARCH=${GOARCH} CC=$(basename "${CC}")"

echo "==> build"
go build ./longfellow/

# Building the package compiles the cgo but does not link it. `go test -c`
# produces a linked binary without running it, which is the only way to find out
# from a container whether the arm64 objects actually resolve against each
# other. It is where a wrong C++ runtime shows up: the NDK has no libstdc++, so
# the cgo directives select -lc++_static for android.
echo "==> link (go test -c, not run)"
go test -c -o "${OUT}" ./longfellow/

echo "==> what came out"
"${TC}/llvm-readelf" -h "${OUT}" | grep -E "Class:|Machine:|Type:"
ls -la "${OUT}"

echo "==> done -- cross-compiled and linked for android/arm64"
