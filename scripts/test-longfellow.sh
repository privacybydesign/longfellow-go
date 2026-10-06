#!/usr/bin/env bash
# Runs longfellow-zk's own test suite against the build this image produced.
#
# Separate from build-longfellow.sh on purpose. #724 step 1 asks that script to
# produce artefacts; a build failure means we cannot ship, whereas a ctest
# failure means upstream's library misbehaves on this toolchain. Different
# questions, different failure meanings, and baking ctest into every image build
# would make each one pay for a ~3 minute suite.
#
# PARALLELISM IS NOT A TUNING KNOB HERE. Measured 2026-09-18 in a 7.6 GB
# container:
#
#   ctest -j 24  ->  9 of 316 "Subprocess killed", one_claim took 164 s
#   ctest -j 2   ->  all pass,                     one_claim took  41 s
#
# The ZK tests hold hundreds of MB each -- two of them together were 2.2 GB
# resident -- so a high -j does not fail gracefully, it gets the OOM killer.
# Slower AND more likely to pass is the counter-intuitive part worth writing down.
# Scale JOBS to memory, not to cores.
set -euo pipefail

BUILD="${BUILD:-/src/longfellow-zk/clang-build-release}"
JOBS="${JOBS:-2}"

echo "==> ctest -j ${JOBS} (see the header before raising this)"
cd "${BUILD}"
exec ctest -j "${JOBS}" "$@"
