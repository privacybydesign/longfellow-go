#!/usr/bin/env bash
# Upstream's own mdoc test suite against the patched library. A memory saving
# that breaks proving is not a saving. JOBS stays low: two ZK tests together
# hold 2.2 GB, so a high -j gets OOM-killed rather than failing an assertion.
set -euo pipefail
SRC=/src/longfellow-zk
BUILD=$SRC/clang-build-release
B=/mem/blocks
R="perl /mem/replace.pl"

$R $SRC/lib/circuits/mdoc/mdoc_decompress.cc $B/a0.old $B/a0.new 1
$R $SRC/lib/circuits/mdoc/mdoc_decompress.cc $B/a1.old $B/a1.new 1
$R $SRC/lib/circuits/mdoc/mdoc_zk.cc         $B/a2.old $B/a2.new 1
$R $SRC/lib/circuits/mdoc/mdoc_zk.cc         $B/a3.old $B/a3.new 1
$R $SRC/lib/circuits/mdoc/mdoc_circuit_id.cc $B/a3.old $B/a3.new 1
$R $SRC/lib/circuits/mdoc/mdoc_zk.cc         $B/b1.old $B/b1.new 1
$R $SRC/lib/circuits/mdoc/mdoc_zk.cc         $B/b2.old $B/b2.new 1

cmake --build "$BUILD" -j 4 >/tmp/build.log 2>&1 || { tail -40 /tmp/build.log; exit 1; }
cd "$BUILD"
ctest -j 2 -R 'mdoc|Mdoc|circuit' --output-on-failure 2>&1 | tail -40
