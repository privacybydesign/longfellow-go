#!/usr/bin/env bash
# Two candidate fixes to longfellow's peak RSS, applied and measured in isolation.
#
#   A. right-size the zstd output buffer  (prover, verifier and circuit_id)
#   B. free the buffer after parsing       (verifier only; the prover already does)
#
# Baseline first, from the unmodified library already installed in the image.
set -euo pipefail
SRC=/src/longfellow-zk
BUILD=$SRC/clang-build-release
B=/mem/blocks
C1=/circuits/6_1_4096_2945_137e5a75ce72735a37c8a72da1a8a0a5df8d13365c2ae3d2c2bd6a0e7197c7c6
C2=/circuits/6_2_4025_2945_b4bb6f01b7043f4f51d8302a30b36e3d4d2d0efc3c24557ab9212ad524a9764e
R="perl /mem/replace.pl"

build_probe() {
  clang++ -std=c++17 -O2 -I$SRC/lib -I$SRC/install/include /mem/mem_probe.cc \
    -L$SRC/install/lib -lmdoc_static -lcrypto -lzstd -lpthread -o /tmp/mem_probe
}
relink() {
  echo "  rebuilding..."
  cmake --build "$BUILD" -j 4 >/tmp/build.log 2>&1 || { tail -30 /tmp/build.log; exit 1; }
  cmake --install "$BUILD" >/dev/null
}

echo "################ BASELINE (upstream, unmodified) ################"
build_probe
/tmp/mem_probe "$C1" 6 1 /mem/results/trace_baseline_1attr.csv 2>/dev/null
  echo
  /tmp/mem_probe "$C2" 6 2 /mem/results/trace_baseline_2attr.csv 2>/dev/null

echo
echo "################ PATCH A: right-size the zstd output buffer ################"
$R $SRC/lib/circuits/mdoc/mdoc_decompress.cc $B/a0.old $B/a0.new 1
$R $SRC/lib/circuits/mdoc/mdoc_decompress.cc $B/a1.old $B/a1.new 1
$R $SRC/lib/circuits/mdoc/mdoc_zk.cc         $B/a2.old $B/a2.new 1
$R $SRC/lib/circuits/mdoc/mdoc_zk.cc         $B/a3.old $B/a3.new 1
$R $SRC/lib/circuits/mdoc/mdoc_circuit_id.cc $B/a3.old $B/a3.new 1
relink
build_probe
/tmp/mem_probe "$C1" 6 1 /mem/results/trace_patchA_1attr.csv 2>/dev/null
  echo
  /tmp/mem_probe "$C2" 6 2 /mem/results/trace_patchA_2attr.csv 2>/dev/null

echo
echo "################ PATCH B: + free the buffer after parsing (verifier) ################"
$R $SRC/lib/circuits/mdoc/mdoc_zk.cc $B/b1.old $B/b1.new 1
$R $SRC/lib/circuits/mdoc/mdoc_zk.cc $B/b2.old $B/b2.new 1
relink
build_probe
/tmp/mem_probe "$C1" 6 1 /mem/results/trace_patchB_1attr.csv 2>/dev/null
  echo
  /tmp/mem_probe "$C2" 6 2 /mem/results/trace_patchB_2attr.csv 2>/dev/null
