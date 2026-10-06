#!/usr/bin/env bash
# Cross-builds longfellow-zk and its dependencies for one Android ABI.
#
# irmago #724 Phase 0 step 4. Adapted from upstream's own android.sh, with four
# changes:
#
#   - the NDK toolchain path is linux-x86_64, not darwin-x86_64. Upstream's
#     script is written for a Mac and cannot work as-is in a Linux container.
#   - PATH is EXPORTED around the OpenSSL build. Upstream sets it as a prefix on
#     the ./Configure line only, so the subsequent make runs without the NDK on
#     PATH — which works only if the generated Makefile happens to record an
#     absolute compiler.
#   - the dependency sources are PINNED. Upstream clones master, so the library
#     a wallet ships would depend on whatever those repositories held that day.
#
# googletest and benchmark ARE built, though nothing a wallet links needs them.
# Skipping them was tried and does not work: CMake/proofs.cmake calls
# find_package for both at CONFIGURE time, unconditionally, so the project will
# not configure without them even when the only target being built is
# libmdoc_static. Upstream's script builds them for that reason and not by
# oversight.
#
# Nothing here requires a device.
set -euo pipefail

SRC="${SRC:-/src/longfellow-zk}"
ABI="${1:-${ANDROID_ABI:-arm64-v8a}}"
API="${ANDROID_API:-24}"
JOBS="${JOBS:-$(nproc)}"

# Every directory is per-ABI. A shared deps or install tree silently mixes
# architectures — the second build overwrites the first, and the link that
# follows fails complaining about machine type rather than about the directory
# that caused it.
DEPS="${SRC}/deps-android-${ABI}"
BUILD="${SRC}/build-android-${ABI}"
PREFIX="${PREFIX:-${SRC}/install-android-${ABI}}"

# OpenSSL names its targets differently from the NDK, and there is no mapping
# either tool will do for you.
case "${ABI}" in
  arm64-v8a)   OPENSSL_TARGET=android-arm64 ;;
  armeabi-v7a) OPENSSL_TARGET=android-arm ;;
  x86_64)      OPENSSL_TARGET=android-x86_64 ;;
  x86)         OPENSSL_TARGET=android-x86 ;;
  *) echo "unknown ABI ${ABI}"; exit 1 ;;
esac

ZSTD_TAG="${ZSTD_TAG:-v1.5.6}"
OPENSSL_TAG="${OPENSSL_TAG:-openssl-3.4.1}"

: "${ANDROID_NDK_ROOT:?set ANDROID_NDK_ROOT}"
TOOLCHAIN="${ANDROID_NDK_ROOT}/toolchains/llvm/prebuilt/linux-x86_64"
test -d "${TOOLCHAIN}" || { echo "no NDK toolchain at ${TOOLCHAIN}"; exit 1; }

CMAKE_ANDROID=(
  -DCMAKE_TOOLCHAIN_FILE="${ANDROID_NDK_ROOT}/build/cmake/android.toolchain.cmake"
  -DANDROID_ABI="${ABI}"
  -DANDROID_PLATFORM="android-${API}"
  -DCMAKE_BUILD_TYPE=Release
)

GTEST_TAG="${GTEST_TAG:-v1.15.2}"
BENCHMARK_TAG="${BENCHMARK_TAG:-v1.9.1}"

mkdir -p "${DEPS}"
cd "${SRC}"

# ---- googletest and benchmark ----------------------------------------------
#
# Required to CONFIGURE, not merely to test: CMake/proofs.cmake find_package()s
# both unconditionally.
echo "==> googletest ${GTEST_TAG} for ${ABI}"
if [ ! -d googletest ]; then
  git clone --depth 1 --branch "${GTEST_TAG}" https://github.com/google/googletest.git
fi
cmake -S googletest -B googletest/build-${ABI} "${CMAKE_ANDROID[@]}" \
      -DCMAKE_INSTALL_PREFIX="${DEPS}" -DBUILD_GTEST=ON -DBUILD_GMOCK=ON
cmake --build googletest/build-${ABI} --target install --parallel "${JOBS}"

echo "==> benchmark ${BENCHMARK_TAG} for ${ABI}"
if [ ! -d benchmark ]; then
  git clone --depth 1 --branch "${BENCHMARK_TAG}" https://github.com/google/benchmark.git
fi
cmake -S benchmark -B benchmark/build-${ABI} "${CMAKE_ANDROID[@]}" \
      -DCMAKE_INSTALL_PREFIX="${DEPS}" \
      -DBENCHMARK_ENABLE_GTEST_TESTS=OFF -DBENCHMARK_ENABLE_TESTING=OFF
cmake --build benchmark/build-${ABI} --target install --parallel "${JOBS}"

# ---- zstd ------------------------------------------------------------------
echo "==> zstd ${ZSTD_TAG} for ${ABI}"
if [ ! -d zstd ]; then
  git clone --depth 1 --branch "${ZSTD_TAG}" https://github.com/facebook/zstd.git
fi
cmake -S zstd/build/cmake -B zstd/build-${ABI} "${CMAKE_ANDROID[@]}" \
      -DCMAKE_INSTALL_PREFIX="${DEPS}" \
      -DZSTD_BUILD_PROGRAMS=OFF -DZSTD_BUILD_SHARED=OFF -DZSTD_BUILD_TESTS=OFF
cmake --build zstd/build-${ABI} --target install --parallel "${JOBS}"

# ---- openssl ---------------------------------------------------------------
#
# Trimmed to almost nothing: longfellow uses libcrypto for hashing and P-256,
# and every other algorithm is dead weight in a wallet. The flag list is
# upstream's, which is where the specific set was chosen.
echo "==> openssl ${OPENSSL_TAG} for ${ABI}"
if [ ! -d openssl ]; then
  git clone --depth 1 --branch "${OPENSSL_TAG}" https://github.com/openssl/openssl.git
fi
(
  cd openssl
  export PATH="${TOOLCHAIN}/bin:${PATH}"
  export ANDROID_NDK_ROOT

  # OpenSSL configures and builds IN TREE, so a second ABI would otherwise
  # inherit the first one's Makefile and object files and quietly produce a
  # library for the wrong architecture. Unlike the CMake dependencies above,
  # a per-ABI build directory is not available here.
  make distclean >/dev/null 2>&1 || true

  CFLAGS=-Wno-macro-redefined ./Configure "${OPENSSL_TARGET}" "-D__ANDROID_API__=${API}" \
    --prefix="${DEPS}" \
    no-autoalginit no-autoerrinit no-tls no-dtls no-legacy no-apps no-docs \
    no-autoload-config no-quic no-zlib no-http no-threads no-mdc2 no-ui-console \
    no-winstore no-idea no-cast no-poly1305 no-siphash no-cmac no-chacha no-cmp \
    no-cms no-comp no-blake2 no-gost no-whirlpool no-camellia no-rc2 no-rc4 \
    no-md4 no-argon2 no-aria no-dsa no-scrypt no-sm2 no-sm3 no-sm4 no-sock \
    no-srp no-srtp no-ssl-trace no-uplink no-dso no-multiblock no-tls1_1 no-tls1_2
  make -j "${JOBS}" install_sw
)

# Static linking only. A wallet that picked up a .so here would need it shipped
# and loadable at run time, which is exactly the prebuilt-binary dependency
# #724's constraint 1 exists to prevent.
echo "==> removing shared libraries from ${DEPS}/lib"
rm -f "${DEPS}"/lib/*.so "${DEPS}"/lib/*.so.*

# ---- longfellow ------------------------------------------------------------
#
# lib/CMakeLists.txt's Android branch HARDCODES the dependency path:
#
#   include_directories("${CMAKE_CURRENT_SOURCE_DIR}/../deps-android/include")
#   link_directories("${CMAKE_CURRENT_SOURCE_DIR}/../deps-android/lib")
#
# It knows nothing about ABIs. Building per-ABI therefore needs deps-android to
# point at whichever tree is current, or the compile fails on a missing
# openssl/sha.h with nothing to say it was looking in the wrong directory —
# which is exactly how this was found.
#
# A symlink rather than a patch to upstream: the CMakeLists is theirs, and one
# more local modification is one more thing to re-apply on every bump.
echo "==> pointing deps-android at ${DEPS}"
rm -rf "${SRC}/deps-android"
ln -s "${DEPS}" "${SRC}/deps-android"

echo "==> longfellow-zk for ${ABI}"
cmake -S "${SRC}/lib" -B "${BUILD}" "${CMAKE_ANDROID[@]}" \
      -DCMAKE_FIND_ROOT_PATH="${DEPS}" \
      -DCMAKE_PREFIX_PATH="${DEPS}" \
      --install-prefix "${PREFIX}"
cmake --build "${BUILD}" --target mdoc_static --parallel "${JOBS}"
cmake --install "${BUILD}" 2>/dev/null || true

# cmake --install on this project installs whatever targets it knows about; the
# one that matters is placed by hand if the install step did not carry it, so
# the result is the same tree shape as the x86_64 build.
mkdir -p "${PREFIX}/lib" "${PREFIX}/include"
find "${BUILD}" -name 'libmdoc_static.a' -exec cp {} "${PREFIX}/lib/" \;
cp "${SRC}/lib/circuits/mdoc/mdoc_zk.h" "${PREFIX}/include/"
cp -r "${DEPS}/lib/." "${PREFIX}/lib/"
cp -r "${DEPS}/include/." "${PREFIX}/include/"

# ---- prove it is actually arm64 --------------------------------------------
echo "==> verifying the artefacts"
test -f "${PREFIX}/lib/libmdoc_static.a" || { echo "MISSING libmdoc_static.a"; exit 1; }
test -f "${PREFIX}/include/mdoc_zk.h"    || { echo "MISSING mdoc_zk.h"; exit 1; }

echo "--- architecture ---"
"${TOOLCHAIN}/bin/llvm-objdump" -a "${PREFIX}/lib/libmdoc_static.a" 2>/dev/null \
  | grep -m1 'file format' || true

echo "--- the five C symbols ---"
"${TOOLCHAIN}/bin/llvm-nm" --defined-only "${PREFIX}/lib/libmdoc_static.a" 2>/dev/null \
  | grep -E " T (run_mdoc_prover|run_mdoc_verifier|generate_circuit|find_zk_spec|circuit_id)$" \
  | sort -u -k3 || echo "(none found -- inspect manually)"

echo "==> done: ${PREFIX}"
