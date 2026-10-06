#!/usr/bin/env bash
# Builds the patched library at three optimisation levels and a probe against
# each, to test whether compiler optimisation moves peak RSS.
#
# The prediction is that it does not: the memory is heap allocations sized from
# the circuit file at run time, not code. -O3 might even cost a little RSS through
# inlining and unrolling enlarging the text segment. Worth measuring rather than
# asserting.
#
# Only longfellow itself is rebuilt. OpenSSL, zstd, googletest and benchmark are
# held constant, so the optimisation level is the single variable.
#
#   docker run --rm -v <mem>:/mem:ro -v <out>:/out longfellow-android build-opt-comparison.sh
set -euo pipefail

SRC="${SRC:-/src/longfellow-zk}"
OUT="${OUT:-/out}"
MEM="${MEM:-/mem}"
ABI="${ANDROID_ABI:-arm64-v8a}"
API="${ANDROID_API:-24}"
JOBS="${JOBS:-$(nproc)}"
DEPS="${SRC}/deps-android-${ABI}"
TC="${ANDROID_NDK_ROOT}/toolchains/llvm/prebuilt/linux-x86_64/bin"

mkdir -p "${OUT}"

# The comparison must be against the library we actually ship, which is the
# patched one — otherwise it measures optimisation against a defect we have
# already fixed.
echo "==> applying memprofile patches"
B="${MEM}/blocks"; R="perl ${MEM}/replace.pl"
$R "${SRC}/lib/circuits/mdoc/mdoc_decompress.cc" "$B/a0.old" "$B/a0.new" 1
$R "${SRC}/lib/circuits/mdoc/mdoc_decompress.cc" "$B/a1.old" "$B/a1.new" 1
$R "${SRC}/lib/circuits/mdoc/mdoc_zk.cc"         "$B/a2.old" "$B/a2.new" 1
$R "${SRC}/lib/circuits/mdoc/mdoc_zk.cc"         "$B/a3.old" "$B/a3.new" 1
$R "${SRC}/lib/circuits/mdoc/mdoc_circuit_id.cc" "$B/a3.old" "$B/a3.new" 1
$R "${SRC}/lib/circuits/mdoc/mdoc_zk.cc"         "$B/b1.old" "$B/b1.new" 1
$R "${SRC}/lib/circuits/mdoc/mdoc_zk.cc"         "$B/b2.old" "$B/b2.new" 1

# deps-android is what lib/CMakeLists.txt hardcodes; point it at this ABI's tree.
rm -rf "${SRC}/deps-android"
ln -s "${DEPS}" "${SRC}/deps-android"

for OPT in O3 O2 Os; do
  echo
  echo "############ -${OPT}"
  build="${SRC}/build-opt-${OPT}"
  prefix="${SRC}/install-opt-${OPT}"

  cmake -S "${SRC}/lib" -B "${build}" \
    -DCMAKE_TOOLCHAIN_FILE="${ANDROID_NDK_ROOT}/build/cmake/android.toolchain.cmake" \
    -DANDROID_ABI="${ABI}" -DANDROID_PLATFORM="android-${API}" \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_CXX_FLAGS_RELEASE="-${OPT} -DNDEBUG" \
    -DCMAKE_C_FLAGS_RELEASE="-${OPT} -DNDEBUG" \
    -DCMAKE_FIND_ROOT_PATH="${DEPS}" -DCMAKE_PREFIX_PATH="${DEPS}" >/dev/null
  cmake --build "${build}" --target mdoc_static --parallel "${JOBS}" >/dev/null

  mkdir -p "${prefix}/lib" "${prefix}/include"
  find "${build}" -name 'libmdoc_static.a' -exec cp {} "${prefix}/lib/" \;
  cp "${DEPS}"/lib/libcrypto.a "${DEPS}"/lib/libzstd.a "${prefix}/lib/"
  cp "${SRC}/lib/circuits/mdoc/mdoc_zk.h" "${prefix}/include/"

  "${TC}/aarch64-linux-android${API}-clang++" -std=c++17 "-${OPT}" -static-libstdc++ \
    -I"${SRC}/lib" -I"${prefix}/include" "${MEM}/mem_probe.cc" \
    -L"${prefix}/lib" -lmdoc_static -lcrypto -lzstd -llog \
    -o "${OUT}/mem_probe_${OPT}"

  printf '%-6s libmdoc_static.a %s  probe %s\n' \
    "-${OPT}" \
    "$(stat -c %s "${prefix}/lib/libmdoc_static.a")" \
    "$(stat -c %s "${OUT}/mem_probe_${OPT}")"
done

echo
echo "==> text segment size of each probe (the only thing -O can move)"
for OPT in O3 O2 Os; do
  printf '%-6s ' "-${OPT}"
  "${TC}/llvm-size" "${OUT}/mem_probe_${OPT}" | tail -1
done
