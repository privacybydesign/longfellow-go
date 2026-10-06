#!/usr/bin/env bash
# Builds longfellow-zk from source and installs the artefacts the Go binding
# links against. #724 Phase 0 step 1's deliverable.
#
# Success means an install tree carrying BOTH of:
#   lib/libmdoc_static.a   - what cgo's -lmdoc_static resolves to
#   include/mdoc_zk.h      - the C ABI
#
# Run inside the build container:
#   docker run --rm -v longfellow-install:/src/longfellow-zk/install \
#     longfellow-build build-longfellow.sh
set -euo pipefail

SRC="${SRC:-/src/longfellow-zk}"
BUILD="${BUILD:-${SRC}/clang-build-release}"
PREFIX="${PREFIX:-${SRC}/install}"
JOBS="${JOBS:-$(nproc)}"

echo "==> configuring (Release, clang++, prefix ${PREFIX})"
# Verbatim from longfellow-zk's README "Building manually", with the prefix made
# explicit so the install tree is where the cgo directives expect it: Google's
# own binding uses -L../../install/lib -I../../install/include relative to
# reference/verifier-service/server/zk.
CXX=clang++ cmake -D CMAKE_BUILD_TYPE=Release \
  -S "${SRC}/lib" -B "${BUILD}" --install-prefix "${PREFIX}"

echo "==> building with ${JOBS} jobs"
cmake --build "${BUILD}" -j "${JOBS}"

echo "==> installing"
cmake --install "${BUILD}"

echo "==> verifying the two artefacts that matter"
test -f "${PREFIX}/lib/libmdoc_static.a" || { echo "MISSING ${PREFIX}/lib/libmdoc_static.a"; exit 1; }
test -f "${PREFIX}/include/mdoc_zk.h"    || { echo "MISSING ${PREFIX}/include/mdoc_zk.h"; exit 1; }

echo "==> checking the C ABI is exported, circuit_id included"
# circuit_id is the one Multipaz's prebuilt libzkp.so does NOT export -- only a
# mangled C++ template instantiation of it -- which is why load-time circuit
# hash verification was thought impossible. Google's own Go code calls it, so a
# source build should have it. If this list is short, say so rather than
# assuming.
nm --defined-only "${PREFIX}/lib/libmdoc_static.a" 2>/dev/null \
  | grep -E " T (run_mdoc_prover|run_mdoc_verifier|generate_circuit|find_zk_spec|circuit_id)$" \
  | sort -u -k3 || echo "(none found -- inspect manually)"

echo "==> done: ${PREFIX}"
