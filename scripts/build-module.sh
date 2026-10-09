#!/usr/bin/env bash
# Builds and tests the Go module against the longfellow library installed in the
# image. irmago #724 Phase 1's deliverable.
#
# Run inside the build container. irmago is fetched as an ordinary module
# dependency (see go.mod), so only this checkout and a circuit directory mount:
#
#   docker run --rm \
#     -v D:\Yivi\longfellow-go:/work/longfellow-go \
#     -v <circuits>:/circuits:ro \
#     -w /work/longfellow-go -e LONGFELLOW_CIRCUITS=/circuits \
#     longfellow-build build-module.sh
#
# The link paths are NOT baked into the cgo directives. Google's reference
# binding writes -L../../install/lib, which resolves only from its own directory
# in its own checkout; #724 asks for configurable link paths, so they come from
# CGO_CFLAGS/CGO_LDFLAGS, which the image sets from LONGFELLOW_INSTALL.
set -euo pipefail

: "${LONGFELLOW_INSTALL:=/src/longfellow-zk/install}"
export CGO_CFLAGS="${CGO_CFLAGS:--I${LONGFELLOW_INSTALL}/include}"
export CGO_LDFLAGS="${CGO_LDFLAGS:--L${LONGFELLOW_INSTALL}/lib}"

echo "==> library at ${LONGFELLOW_INSTALL}"
test -f "${LONGFELLOW_INSTALL}/lib/libmdoc_static.a" || {
  echo "MISSING ${LONGFELLOW_INSTALL}/lib/libmdoc_static.a -- run build-longfellow.sh first"; exit 1; }

echo "==> go version: $(go version)"
echo "==> gofmt"
unformatted="$(gofmt -l longfellow/ cmd/)"
if [ -n "${unformatted}" ]; then echo "unformatted: ${unformatted}"; exit 1; fi

echo "==> vet"
go vet ./...

echo "==> build"
go build ./...

# Circuits cannot all be generated -- generate_circuit emits only the library's
# newest revision while readers ask for older ones -- so the proving tests need
# a directory of them. Locally that is a checkout; in CI it is what
# generate-circuits.sh produced.
#
# The default is to skip those tests when no directory is given, which keeps a
# quick `go test` usable on a machine with no circuits. That default is wrong
# for CI: a run that silently skips every proving test reports green while
# testing none of them. So CI sets LONGFELLOW_STRICT=1 and the absence becomes
# a failure.
if [ -z "${LONGFELLOW_CIRCUITS:-}" ]; then
  if [ "${LONGFELLOW_STRICT:-0}" = "1" ]; then
    echo "LONGFELLOW_CIRCUITS is not set and LONGFELLOW_STRICT=1." >&2
    echo "Every proving test would skip and this run would report green having proved nothing." >&2
    echo "Run generate-circuits.sh first, or point LONGFELLOW_CIRCUITS at a circuit directory." >&2
    exit 1
  fi
  echo "==> test (no LONGFELLOW_CIRCUITS set: the proving tests will skip)"
else
  echo "==> test (circuits: ${LONGFELLOW_CIRCUITS})"
  if [ "${LONGFELLOW_CIRCUITS_GENERATED:-0}" = "1" ]; then
    echo "    generated from the pinned source: only the library's newest revision is present,"
    echo "    so tests needing an older one skip. Look for NOT COVERED in the output."
  fi
fi
go test ./longfellow/ -count=1 "$@"

echo "==> done"
