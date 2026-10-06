# Peak RSS: where longfellow's memory actually goes, and two patches that cut 30%

Measured 2026-09-22 against the pinned source build
(`61a8a735964d1b22bccf79bf14ef6767249cdf92`, `v0.9-177`).

> **Platform: unless a number says otherwise, it is linux/amd64**, measured
> inside the `longfellow-build` container (`../Dockerfile`, `ubuntu:24.04`).
>
> **arm64 has now been measured** — see "Confirmed on arm64" at the end:
> 210.9 MB baseline, 139.9 MB patched, on the same phone the 445 MB came from.
> Quote those figures for arm64 and the table under "Result" for x86_64, and do
> not mix them. A peer session once turned a summary of this work into "148 MB on
> a Dimensity 8100", which was true of neither platform at the time.
>
> The **445 MB** remains a whole-app measurement and is not comparable to either
> column; it is the app plus the library, not the library.

The question was irmago #724's risk-table entry — *"88 MB resident during
proving"* — against a device measurement of **445 MB** peak RSS on a Dimensity
8100. Neither number turned out to describe the library.

## What was measured

`mem_probe.cc` links the library directly: no JVM, no Go, no cgo. It loads a
shipped circuit, proves, verifies, and reports `VmRSS`/`VmHWM` at each step while
a background thread samples `/proc/self/status` every 10 ms. The traces in
`results/` are those samples.

```
docker run --rm -v D:\Yivi\longfellow-go\memprofile:/mem \
  -v D:\Yivi\multipaz\multipaz-longfellow\src\commonMain\circuits:/circuits:ro \
  longfellow-build bash /mem/patch_and_measure.sh
```

That one command measures the unmodified library, applies patch A and re-measures,
applies patch B and re-measures — all in one container, because the image is
`--rm` and a patched build does not survive it. `ctest_patched.sh` runs upstream's
own mdoc suite against the fully patched library.

## Result

| v6 circuit | baseline | +A | +A and B |
|---|---|---|---|
| 1 attribute | 211.8 MB | 171.2 MB | **147.7 MB** |
| 2 attributes | 213.5 MB | 177.9 MB | **154.4 MB** |

Upstream's suite on the patched library: **23/23 pass in 190 s**
(`results/ctest_patched.txt`), negative tests — `wrong_witness`, `bad_proofs`,
`attr_mismatch` — included. Proof sizes and timings unchanged.

**So the device figure was mostly not longfellow.** The library's own peak is
212 MB; the other ~230 MB of that 445 was ART, Compose and graphics in a Multipaz
demo app. The risk-table entry should read **~150–210 MB on top of whatever the
host app already holds**.

## The defect

Three sites — `mdoc_zk.cc:437` (prover), `mdoc_zk.cc:599` (verifier) and
`mdoc_circuit_id.cc:50` — do:

```cpp
size_t len = kCircuitSizeMax;        // 130,000,000
std::vector<uint8_t> bytes(len);     // value-initialised: 130 MB written, so 130 MB resident
size_t full_size = decompress(bytes, bcp, bcsz);   // v6/1-attr needs 87.7 MB
```

**Patch A** sizes the buffer from `ZSTD_getFrameContentSize` instead of from the
upper bound, falling back to the bound when the frame declares no size.
**Patch B** scopes the verifier's buffer so it is freed once `CircuitReader` has
built the two circuits — which is what the prover already does, and the reason
verify peaked higher than prove.

The blocks in `blocks/` are literal before/after text; `replace.pl` refuses to
apply one that does not match exactly once, so a patch can never land quietly on
drifted source.

## What the peak is made of — not what the risk table says

Proving has **two** peaks: the parse phase (decompress buffer + reader) and the
proving phase. After the patches the proving phase binds at ~148 MB, of which the
parsed circuit is only ~40 MB and the prover's working set ~100 MB. The 87.7 MB
decompressed circuit is transient, not resident.

Attribute scaling is mild: a second attribute costs **+7 MB and ~15 ms**. Earlier
notes guessed 2–4 attributes would be much worse; in memory terms they are not.
Decompressed sizes of every shipped circuit, from the zstd frame header:

| attrs | v6 | v7 |
|---|---|---|
| 1 | 83.7 MB | 94.3 MB |
| 2 | 88.4 MB | 99.3 MB |
| 3 | 93.1 MB | 104.3 MB |
| 4 | 97.8 MB | **109.3 MB** |

≈ +5 MB per attribute, +11–12 MB per version.

## kCircuitSizeMax: no published rationale, and the coupling is the real problem

Introduced 2025-04-30 at 150,000,000 with its current comment; tightened to
130,000,000 on 2026-03-31. Every commit in google/longfellow-zk is a squashed
`Copybara` import carrying no message, so **there is no rationale to find** — do
not claim to know why 130. 130 MB simply sits 13% above the largest circuit that
exists.

The constant does two unrelated jobs: it is the allocation size *and* the
acceptance ceiling. That coupling is what forces their comment — *"better to make
this bound tight to avoid memory failure in the resource restricted Android
gmscore environment"* — because while the two are one number, cutting memory
means refusing circuits, and a circuit above the bound does not degrade, it fails
to load. Patch A decouples them.

**Patch A buys the memory half and none of the integrity half.** It sizes the
buffer from `ZSTD_getFrameContentSize` — a value declared by the circuit bytes
themselves, which are unauthenticated at that moment, because integrity cannot
come first: `circuit_id()` has to decompress and parse before the hash exists. So
an honest file now costs what it actually needs, while a hostile one declaring
129 MB still gets 129 MB. The worst case is unchanged; only the ordinary case
improved.

Raising the bound afterwards is therefore cheap but not free of risk: it raises
that worst case one-for-one. Low severity while circuits are bundled assets;
materially higher if they are ever fetched at runtime.

**For longfellow-go the answer is to replace the bound, not raise it.** We know
which circuits we ship — `generate_circuit` cannot emit v6, so shipping them is
forced — and each circuit id has exactly one correct decompressed size. Check the
frame content size against the expected size for the id that was requested and
allocate precisely that. Note what makes it a different check rather than more of
patch A: it compares the declared size against what that id *must* decompress to
and refuses on mismatch, instead of believing the declaration. That is what
bounds the worst case, and it is only possible on our side, where the requested
id is known.

That belongs here, over the C ABI; the patch offered upstream should stay the
narrow "stop paying the bound in RSS" change, leaving the ceiling their call.

## Where the load-time peak is paid, on our path specifically

irmago's `ZkSystem.SystemSpecs` contract requires recomputing `circuit_hash` at
load and refusing any circuit whose bytes disagree with the hash it is filed
under. So `circuit_id()` — the third allocation site — runs over every
registered circuit at startup, and patch A cuts that peak too, not only the
proving one. This is a genuine difference from the Multipaz measurement, where
the high-water mark landed on first verify.

The peak is per-circuit and transient rather than cumulative, but only while the
loader holds one decompressed circuit at a time. On a path that registers several
circuits in succession, "only one resident" is doing real work rather than
stating a formality.

---

## Confirmed on arm64 — 2026-09-22

Measured on the device the 445 MB figure came from: **CPH2423 / mt6895
(Dimensity 8100), Android 15, 8 cores, 11.7 GB RAM**. Same v6/1-attribute
circuit, same probe source, two binaries differing only in the library they link.

| peak RSS, v6/1 attr | x86_64 | arm64 |
|---|---|---|
| baseline | 211.8 MB | **210.9 MB** |
| patched (A+B) | 147.7 MB | **139.9 MB** |
| saving | −64 MB (−30%) | **−71 MB (−34%)** |

Two things this settles.

**The patches work on arm64, and slightly better than on x86_64.** Nothing about
them was platform-specific, and the saving is if anything larger.

**The 445 MB was the host app, and that is now measured rather than argued.**
Same phone, same circuit: the library's own peak is **210.9 MB**. The remaining
~235 MB was ART, Compose and graphics in a Multipaz demo app. The earlier
conclusion was inference from an x86_64 comparison; this is the direct
measurement on the same hardware.

Timings, from a later **interleaved** run — A B A B rather than all of A then all
of B, which matters more than it sounds (see the warning below):

| | unpatched | patched |
|---|---|---|
| prove | 2074 ms | 2004 ms |
| verify | 1050 ms | 997 ms |

The patches are slightly **faster** as well as smaller: zeroing 130 MB of pages
costs time, not only space.

> **Never compare timings taken in different sessions on a phone.** The same
> binary measured 1490 ms cold in the morning and 2342 ms after an afternoon of
> benchmarking — 57% slower, purely thermal. A 533 ms "regression" was chased
> here on exactly that mistake and did not exist. Interleave the variants, and
> treat any cross-session delta under ~50% as noise.

## Multipaz's shipped library has the same defect

Their prebuilt `libzkp.so` for arm64-v8a, run through this same probe in the same
interleaved session:

| v6 / 1 attr | prove | verify | peak RSS |
|---|---|---|---|
| Multipaz prebuilt `libzkp.so` | 2074 ms | 1093 ms | **212.7 MB** |
| ours, from source, unpatched | 2074 ms | 1050 ms | 211.0 MB |
| ours, from source, patched | 2004 ms | 997 ms | **139.0 MB** |

Two things follow. Our from-source build is **indistinguishable** from their
prebuilt one — prove medians identical to the millisecond — so building it
ourselves, which #724 requires, costs nothing in speed. And at 212.7 MB their
shipped binary pays the full `kCircuitSizeMax` buffer too, so these patches would
help every longfellow consumer, not just us. That is the argument for sending
them upstream rather than carrying them.

Raw runs: `../androidbench/results/threeway-library.csv`.

Reproduce with `../androidbench/run-on-phone.ps1`. No phone is needed to BUILD
any of this; only to run it.

---

## Why the patches are also FASTER — profiled 2026-09-22

The patched build is 3–9% quicker as well as 35% smaller, which looked like a
bonus until `simpleperf` explained it: **~22% of a prove+verify run is kernel
time** — page faults and memory management — plus another 1.8% in
`__memset_aarch64`.

That is the memory cost appearing in *time units*. Value-initialising 130 MB
instead of 88 MB means 42 MB of extra pages touched, faulted and zeroed, and the
kernel charges for every one of them. Cutting the allocation cuts the work.

So the two patches are not a space-versus-speed trade. They are strictly better
on both axes, and the mechanism is measured rather than assumed.

Full breakdown, including why Ligero is only 1.7% and why circuit parsing costs
four times as much as it: `../androidbench/` and the figure at
`longfellow_prove_profile_android.svg`.

### One correction this profile forced

An earlier note here and in several summaries put circuit load at **"45% of
prove and 66% of cold verify"**. On arm64 decompress+parse is **~15% of prove**.
That figure was used twice to argue for a parsed-circuit handle in the C ABI;
the argument does not survive the measurement, and the proposal was dropped —
it would save ~375 ms of ~2545 ms, only on the second and later proof in a
process, at a cost of ~42 MB held resident. A wallet proves once per
presentation.
