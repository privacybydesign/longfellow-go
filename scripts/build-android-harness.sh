#!/usr/bin/env bash
# Builds everything the on-device measurement needs, for android/arm64.
#
# Produces three binaries in ${OUT} (default /out, mount it from the host):
#
#   mem_probe_baseline   peak-RSS probe against the UNPATCHED library
#   mem_probe_patched    the same probe against the library with memprofile's
#                        two patches applied
#   longfellow.test      the Go test binary: the whole module, including a
#                        complete isomdoc session
#
# Why both probes: memprofile/ measured 212 MB -> 148 MB on x86_64 and that has
# never been checked on arm64. Two binaries from one source, differing only in
# the library they link, is the only way to attribute a difference to the patches
# rather than to the platform.
#
# THAT COMPARISON IS DONE, AND THIS SCRIPT NO LONGER RUNS AS WRITTEN.
#
# Its results are in androidbench/results/cold-runs.csv, under a Variant column,
# and they are what justified shipping the memory patches. Those patches now
# live in patches/ and are applied by the Dockerfile, so ${SRC} arrives already
# carrying them: the replace.pl calls below will die with "found 0 occurrences".
# That failure is the correct behaviour -- loudly wrong beats silently building
# two identical libraries and reporting that the patches achieve nothing.
#
# To redo the comparison, build the baseline from a checkout with patches 0002
# and 0003 reverted (git apply -R) and leave 0001 in place: it exposes a C entry
# point and has no bearing on memory.
#
# Everything here is a build. The device is needed only to RUN the output.
set -euo pipefail

SRC="${SRC:-/src/longfellow-zk}"
OUT="${OUT:-/out}"
MEM="${MEM:-/mem}"
API="${ANDROID_API:-24}"
ABI="${ANDROID_ABI:-arm64-v8a}"
JOBS="${JOBS:-$(nproc)}"

ABI="${ANDROID_ABI:-arm64-v8a}"
BASE_PREFIX="${SRC}/install-android-${ABI}"
PATCHED_PREFIX="${SRC}/install-android-${ABI}-patched"
PATCHED_BUILD="${SRC}/build-android-${ABI}-patched"
DEPS="${SRC}/deps-android"

: "${ANDROID_NDK_ROOT:?set ANDROID_NDK_ROOT}"
TC="${ANDROID_NDK_ROOT}/toolchains/llvm/prebuilt/linux-x86_64/bin"
CXX_ANDROID="${TC}/aarch64-linux-android${API}-clang++"

mkdir -p "${OUT}"
test -f "${BASE_PREFIX}/lib/libmdoc_static.a" || {
  echo "MISSING the arm64 library -- this image should have been built from Dockerfile.android"; exit 1; }
test -f "${MEM}/mem_probe.cc" || { echo "MISSING ${MEM}/mem_probe.cc -- mount memprofile at ${MEM}"; exit 1; }

build_probe() {
  local prefix="$1" out="$2"
  "${CXX_ANDROID}" -std=c++17 -O2 -static-libstdc++ \
    -I"${SRC}/lib" -I"${prefix}/include" \
    "${MEM}/mem_probe.cc" \
    -L"${prefix}/lib" -lmdoc_static -lcrypto -lzstd -llog \
    -o "${out}"
  # -llog because the Android build of longfellow's util/log.cc calls
  # __android_log_print. The Go binary picks liblog up through the NDK's default
  # link line; a bare clang++ invocation does not.
}

# ---- 1. the probe against the library as upstream ships it -----------------
echo "==> mem_probe against the unpatched arm64 library"
build_probe "${BASE_PREFIX}" "${OUT}/mem_probe_baseline"

# ---- 2. apply memprofile's patches and rebuild the library -----------------
#
# The deps (OpenSSL, zstd, gtest, benchmark) are already built and untouched, so
# this rebuilds only longfellow itself.
echo "==> applying memprofile patches"
B="${MEM}/blocks"
R="perl ${MEM}/replace.pl"
$R "${SRC}/lib/circuits/mdoc/mdoc_decompress.cc" "$B/a0.old" "$B/a0.new" 1
$R "${SRC}/lib/circuits/mdoc/mdoc_decompress.cc" "$B/a1.old" "$B/a1.new" 1
$R "${SRC}/lib/circuits/mdoc/mdoc_zk.cc"         "$B/a2.old" "$B/a2.new" 1
$R "${SRC}/lib/circuits/mdoc/mdoc_zk.cc"         "$B/a3.old" "$B/a3.new" 1
$R "${SRC}/lib/circuits/mdoc/mdoc_circuit_id.cc" "$B/a3.old" "$B/a3.new" 1
$R "${SRC}/lib/circuits/mdoc/mdoc_zk.cc"         "$B/b1.old" "$B/b1.new" 1
$R "${SRC}/lib/circuits/mdoc/mdoc_zk.cc"         "$B/b2.old" "$B/b2.new" 1

echo "==> rebuilding longfellow for ${ABI} with the patches"
cmake -S "${SRC}/lib" -B "${PATCHED_BUILD}" \
      -DCMAKE_TOOLCHAIN_FILE="${ANDROID_NDK_ROOT}/build/cmake/android.toolchain.cmake" \
      -DANDROID_ABI="${ABI}" -DANDROID_PLATFORM="android-${API}" \
      -DCMAKE_BUILD_TYPE=Release \
      -DCMAKE_FIND_ROOT_PATH="${DEPS}" -DCMAKE_PREFIX_PATH="${DEPS}" >/dev/null
cmake --build "${PATCHED_BUILD}" --target mdoc_static --parallel "${JOBS}" >/dev/null

mkdir -p "${PATCHED_PREFIX}/lib" "${PATCHED_PREFIX}/include"
find "${PATCHED_BUILD}" -name 'libmdoc_static.a' -exec cp {} "${PATCHED_PREFIX}/lib/" \;
cp -r "${DEPS}/lib/." "${PATCHED_PREFIX}/lib/"
cp -r "${DEPS}/include/." "${PATCHED_PREFIX}/include/"
cp "${SRC}/lib/circuits/mdoc/mdoc_zk.h" "${PATCHED_PREFIX}/include/"

echo "==> mem_probe against the patched arm64 library"
build_probe "${PATCHED_PREFIX}" "${OUT}/mem_probe_patched"

# ---- 3. the Go module, as a runnable test binary ---------------------------
#
# Built against the UNPATCHED library: what this measures is our own stack, and
# mixing in a patched dependency would make the timings answer two questions at
# once.
echo "==> the Go test binary"
(
  cd /work/longfellow-go
  export GOOS=android GOARCH=arm64 CGO_ENABLED=1
  export CC="${TC}/aarch64-linux-android${API}-clang"
  export CXX="${CXX_ANDROID}"
  export CGO_CFLAGS="-I${BASE_PREFIX}/include"
  export CGO_LDFLAGS="-L${BASE_PREFIX}/lib"
  go test -c -o "${OUT}/longfellow.test" ./longfellow/
)

echo "==> what came out"
for binary in mem_probe_baseline mem_probe_patched longfellow.test; do
  printf '%-22s ' "${binary}"
  "${TC}/llvm-readelf" -h "${OUT}/${binary}" | awk '/Machine:/{m=$2} END{printf "%s ", m}'
  stat -c '%s bytes' "${OUT}/${binary}"
done

echo "==> done -- push ${OUT} to the device"
