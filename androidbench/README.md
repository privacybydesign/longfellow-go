# On-device measurement harness

Two questions this exists to answer, both still open after Phase 0 step 4:

1. **Real arm64 timings for our own stack.** The 2.3 s prove / 1.2 s verify
   figures on record came from Multipaz's Kotlin driving their prebuilt
   `libzkp.so`. Nothing of ours has ever run on an arm64 device.
2. **The first arm64 peak-RSS numbers, patched vs unpatched.** `memprofile/`
   measured 212 MB -> 148 MB on x86_64 and says plainly that no arm64 figure
   exists. This produces one.

## Why this is simpler than the last on-device run

Everything pushed is a plain executable needing only `liblog`, `libdl` and
`libc` — every Android device has those, and OpenSSL, zstd and libc++ are
statically linked in. So: no APK, no Gradle, no `androidInstrumentedTest` source
set, no patched Multipaz checkout. That route cost four failed runs (the
installer parking on a debugger, WSL baking `/mnt/d/...` into Kotlin metadata,
Gradle leaving a package installed with no instrumentation declared, ColorOS
swallowing log output).

## Running it

```
# 1. build the three arm64 binaries (no device needed)
docker run --rm \
  -v D:\Yivi\longfellow-go:/work/longfellow-go \
  -v D:\Yivi\longfellow-go\memprofile:/mem:ro \
  -v D:\Yivi\longfellow-go\androidbench\bin:/out \
  -w /work/longfellow-go longfellow-android build-android-harness.sh

# 2. connect the phone, enable USB debugging, accept the RSA prompt
.\androidbench\run-on-phone.ps1
```

`bin/` holds:

| | |
|---|---|
| `mem_probe_baseline` | peak-RSS probe against the **unpatched** library |
| `mem_probe_patched` | the same probe against the library with memprofile's two patches |
| `longfellow.test` | the Go module's whole suite, including a full `isomdoc` session |

Both probes are built from one source and differ only in the library they link,
which is what lets a difference be attributed to the patches rather than to the
platform.

The Go binary links the **unpatched** library on purpose: it measures our stack,
and a patched dependency would make its timings answer two questions at once.

## Reading the results

`results/` gets `device.json`, `mem_probe_*.txt`, `trace_*.csv` (RSS sampled
every 10 ms) and `gotest.txt`.

Compare against x86_64, from `memprofile/README.md`:

| | x86_64 baseline | x86_64 patched |
|---|---|---|
| peak RSS, v6/1 attr | 211.8 MB | 147.7 MB |
| prove | 711 ms | — |
| verify | 413 ms | — |

## Two cautions

**Thermal throttling flatters whatever runs first.** If the two probes disagree
by a little, re-run with the order reversed before believing it.

**Some vendor ROMs refuse exec from `/data/local/tmp` under SELinux.** The script
checks this before measuring anything and says so; the fallback is the app-based
route, which is known to work but costs everything in the list above.

## Profiling

`bin/simpleperf` is extracted from the NDK
(`$ANDROID_NDK_ROOT/simpleperf/bin/android/arm64/simpleperf`) by
`scripts/build-android-harness.sh`'s sibling step; push it alongside the probes.

```
adb push bin/simpleperf /data/local/tmp/lfbench/ && adb shell chmod 755 …/simpleperf

# where the time goes
./simpleperf record -e cpu-clock -f 1000 -g -o perf.data ./mem_probe_patched circuits/<v6/1> 6 1
./simpleperf report -i perf.data --sort symbol

# algorithmic or memory-bound? IPC and miss rate per symbol
./simpleperf record -e cpu-cycles,instructions,cache-misses,cache-references -o hw.data ./mem_probe_patched …
./simpleperf report -i hw.data --sort symbol
```

`perf_event_paranoid` is `-1` on this device, so no root is needed. At `-f 1000`
one sample is one millisecond of CPU, which makes the sample column readable as
milliseconds directly.

Findings from the 2026-09-22 run — sumcheck 34%, kernel 22%, parsing 12%,
**Ligero 1.7%**, and `dedup` algorithmic rather than cache-bound — are in
`../memprofile/README.md` and the figure
`longfellow_prove_profile_android.svg`.
