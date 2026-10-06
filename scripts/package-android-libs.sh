#!/usr/bin/env bash
# Builds longfellow for every Android ABI and packages the result the way
# irmamobile's bind_go.sh already consumes a native dependency.
#
# irmamobile links prebuilt static SQLCipher + OpenSSL exactly this way, because
# "gomobile doesn't support per-ABI CGO flags and the linker rejects .a files for
# the wrong architecture" — so it runs gomobile bind once per ABI with different
# -I/-L and merges the resulting libgojni.so files into one AAR. Adding longfellow
# is a second set of flags on those same invocations, not new machinery, PROVIDED
# the tarball has the layout that script expects:
#
#   <name>-<version>-android.tar.gz
#     android/include/…              headers, shared across ABIs
#     android/<abi>/lib/*.a          one set of static libraries per ABI
#
# where <abi> is arm64-v8a, armeabi-v7a or x86_64.
#
#   docker run --rm -v <out>:/out longfellow-android package-android-libs.sh
#
# NO DEVICE IS REQUIRED. This is a build.
#
# On the #724 constraint: the tarball this produces is a build artefact, not a
# repo member. Constraint 1 forbids a prebuilt binary entering the repo or the
# module graph; a release asset built from pinned source is neither. It does NOT
# satisfy F-Droid, which requires building from source — that build runs this
# script itself rather than downloading its output.
set -euo pipefail

SRC="${SRC:-/src/longfellow-zk}"
OUT="${OUT:-/out}"
ABIS="${ABIS:-arm64-v8a armeabi-v7a x86_64}"
STAGE="$(mktemp -d)"
trap 'rm -rf "${STAGE}"' EXIT

# The version the tarball is named for: longfellow's own pinned commit, short.
# Not a version we invent — what a consumer needs to know is which source this
# came from, and nothing else identifies that.
VERSION="${VERSION:-$(git -C "${SRC}" rev-parse --short HEAD)}"
NAME="longfellow-${VERSION}-android"

echo "==> packaging ${NAME} for: ${ABIS}"
mkdir -p "${STAGE}/android/include"

for abi in ${ABIS}; do
  echo
  echo "############ ${abi}"
  ANDROID_ABI="${abi}" build-longfellow-android.sh "${abi}"

  prefix="${SRC}/install-android-${abi}"
  mkdir -p "${STAGE}/android/${abi}/lib"

  # ONLY what a wallet links. The install tree also holds libgtest, libgmock,
  # libbenchmark and libssl — googletest and benchmark because longfellow's
  # CMakeLists find_package()s them at configure time, and libssl because
  # OpenSSL builds it even configured no-tls. Shipping them added 59 MB to this
  # tarball and put a test framework in an application's link line.
  for lib in libmdoc_static.a libcrypto.a libzstd.a; do
    cp "${prefix}/lib/${lib}" "${STAGE}/android/${abi}/lib/"
  done
done

# One header, once. The consumer is cgo, and longfellow/binding.go includes
# mdoc_zk.h and nothing else — the C++ is linked, not recompiled. The install
# tree's other headers are gtest's, gmock's, benchmark's and OpenSSL's, none of
# which any consumer of this tarball compiles against.
cp "${SRC}/lib/circuits/mdoc/mdoc_zk.h" "${STAGE}/android/include/"

mkdir -p "${OUT}"
tar -czf "${OUT}/${NAME}.tar.gz" -C "${STAGE}" android

# The consumer verifies this before extracting, as bind_go.sh already does for
# SQLCipher. A tarball fetched over the network with no digest to check is a
# supply-chain hole regardless of who built it.
( cd "${OUT}" && sha256sum "${NAME}.tar.gz" > "${NAME}.tar.gz.sha256" )

echo
echo "==> ${OUT}/${NAME}.tar.gz"
cat "${OUT}/${NAME}.tar.gz.sha256"
echo
echo "contents:"
tar -tzf "${OUT}/${NAME}.tar.gz" | grep -E '\.a$|\.h$' | sed 's/^/  /' | head -20
