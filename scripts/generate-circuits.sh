#!/usr/bin/env bash
# Generates the circuits the pinned library is able to emit, for a machine that
# has none of its own -- CI being the case this was written for.
#
#   docker run --rm -v <out>:/circuits -v <repo>:/work/longfellow-go \
#     -w /work/longfellow-go longfellow-build generate-circuits.sh /circuits
#
# READ THIS BEFORE TREATING THE OUTPUT AS A CIRCUIT DIRECTORY. generate_circuit
# emits only the library's newest revision. Every older one -- v6 included, the
# revision the captured EUDI AV reader offered -- is unobtainable from source,
# which is exactly why LONGFELLOW_CIRCUITS points at a directory rather than
# being generated on demand. A test run against this output exercises the full
# prover, verifier, adapter and session path, under a revision no deployed
# reader currently asks for.
#
# The .generated marker this leaves behind is what build-module.sh and the tests
# read to tell "this directory is generated, so older revisions are structurally
# absent" from "someone forgot to mount the circuits".
set -euo pipefail

OUT="${1:-${LONGFELLOW_CIRCUITS:-}}"
if [ -z "${OUT}" ]; then
  echo "usage: generate-circuits.sh <output-directory>" >&2
  exit 2
fi

: "${LONGFELLOW_INSTALL:=/src/longfellow-zk/install}"
export CGO_CFLAGS="${CGO_CFLAGS:--I${LONGFELLOW_INSTALL}/include}"
export CGO_LDFLAGS="${CGO_LDFLAGS:--L${LONGFELLOW_INSTALL}/lib}"

test -f "${LONGFELLOW_INSTALL}/lib/libmdoc_static.a" || {
  echo "MISSING ${LONGFELLOW_INSTALL}/lib/libmdoc_static.a -- run build-longfellow.sh first" >&2
  exit 1; }

echo "==> generating circuits into ${OUT}"
# Each circuit costs about ten seconds to generate, so this is minutes, not
# hours, and it is cached by the caller rather than repeated per test run.
go run ./cmd/gencircuits -out "${OUT}"

# The caller must pass this through to the test run. Without it the tests that
# need a revision the generator cannot emit FAIL rather than skip, which is the
# right default for a directory that is supposed to be complete.
echo
echo "LONGFELLOW_CIRCUITS=${OUT} LONGFELLOW_CIRCUITS_GENERATED=1"
