#!/usr/bin/env bash
# Builds and tests the Go module against the longfellow library installed in the
# image. irmago #724 Phase 1's deliverable.
#
# Run inside the build container, with both checkouts mounted because go.mod
# replaces irmago with ../irmago:
#
#   docker run --rm \
#     -v D:\Yivi\longfellow-go:/work/longfellow-go \
#     -v D:\Yivi\irmago:/work/irmago:ro \
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

# Circuits cannot be generated -- generate_circuit only emits the library's
# newest version while readers ask for older revisions -- so the round-trip tests
# need a directory of them. Without one they skip rather than fail, and the load and
# contract tests still run.
if [ -n "${LONGFELLOW_CIRCUITS:-}" ]; then
  echo "==> test (circuits: ${LONGFELLOW_CIRCUITS})"
else
  echo "==> test (no LONGFELLOW_CIRCUITS set: the proving tests will skip)"
fi
go test ./longfellow/ -count=1 "$@"

echo "==> done"
