# longfellow-go

The cgo module that carries the native zero-knowledge prover, so that irmago
does not have to.

Built for irmago issue **#724** (AV Annex A §A.8 mandates `longfellow-libzk-v1`
proofs). Everything here links **google/longfellow-zk built from source** at the
pinned commit `61a8a735964d1b22bccf79bf14ef6767249cdf92` (`v0.9-177`). No
prebuilt binary enters this module's graph, which is a constraint of #724 rather
than a preference.

---

## What is in this repo

| | |
|---|---|
| `longfellow/` | **the module** — implements irmago's `zk.System` interfaces. The deliverable. |
| `cmd/gencircuits` | writes every circuit the pinned library can emit, for a machine that has none. Cannot produce older revisions -- see below. |
| `cmd/genmapcache` | generates a compiled-in `MapCache` from a circuit directory, so an app that bundles circuits does not pay ~1.2 s per circuit at every launch. |
| `Dockerfile`, `Dockerfile.android`, `scripts/` | the from-source builds: the library (x86_64 and per-ABI Android), the module, the on-device harness, and the release tarball. |
| `.github/workflows/ci.yml` | builds the library from source and runs both suites over it: this module's and upstream's own. irmago runs a complementary job -- see below. |
| `patches/` | changes submitted upstream but not yet in a ref we can pin, applied to the checkout at image-build time. See below. |
| `memprofile/` | peak-RSS measurement: the A/B harness behind the two memory patches, which now ship from `patches/`. See its own README. |
| `androidbench/` | the on-device harness and its measurement record. Binaries and the release tarball are build outputs and stay out of the tree. |

---

## The dependency direction, which is the whole design

```
irmago  ──declares──>  eudi/credentials/mdoc/zk      (leaf, stdlib only)
                              ^
                              │ implements
                       longfellow-go/longfellow      (cgo)
```

**irmago never imports this module.** This module imports irmago's leaf package
and satisfies the interfaces declared there. An application that wants a prover
imports both and wires them together.

That is what keeps irmago buildable with `CGO_ENABLED=0` — `./yivi` cross-builds,
an ordinary `go build` must not demand a ZK toolchain, and the F-Droid build of
the wallet has to be buildable from source. It is also why a build with no prover
is the ordinary state rather than an error: §A.8 says "where the User's device
does not support Zero-Knowledge Proof generation, the AVI SHALL fall back to the
plain ISO mDoc presentation defined in Section A.6."

The boundary is byte-oriented on purpose. If it took an `*mdoc.MDoc` or a
`dcql.DisclosureSelection`, this module would have to import all of irmago to
satisfy one interface and the split would collapse. The adapter that speaks both
languages is `mdoc.ProverSystem`, in irmago.

---

## Building and testing

Everything runs in the `longfellow-build` image, which carries the library, its
install tree and Go 1.27.

```
cd D:/Yivi/longfellow-go
docker build -t longfellow-build .

docker run --rm \
  -v D:\Yivi\longfellow-go:/work/longfellow-go \
  -v D:\Yivi\multipaz\multipaz-longfellow\src\commonMain\circuits:/circuits:ro \
  -w /work/longfellow-go -e LONGFELLOW_CIRCUITS=/circuits \
  longfellow-build build-module.sh
```

irmago is fetched as an ordinary module dependency, pinned to the commit on
its ZKP_Age_Verification branch that carries the zk package (see go.mod).

**Link paths are not baked into the cgo directives.** Google's reference binding
writes `-L../../install/lib`, which resolves only from its own directory in its
own checkout; #724 asks for configurable paths, so they come from
`CGO_CFLAGS`/`CGO_LDFLAGS`, which the image sets from `LONGFELLOW_INSTALL`.

Without `LONGFELLOW_CIRCUITS` the proving tests skip rather than fail. Set
`LONGFELLOW_STRICT=1` to make the absence a failure instead -- CI does, because
a run that silently skips every proving test reports green having proved nothing.

### Circuits on a machine that has none

```
docker run --rm \
  -v D:\Yivi\longfellow-go:/work/longfellow-go \
  -v <out>:/circuits -w /work/longfellow-go \
  longfellow-build generate-circuits.sh /circuits
```

`cmd/gencircuits` writes every circuit the pinned library can emit, then checks
each one loads under the id its spec claims -- something only a generator can
establish, since a loader sees just the bytes it was handed.

**It cannot replace a circuit directory.** `generate_circuit` emits the
library's newest revision only; every older one -- v6 included, the revision the
captured EUDI AV reader offered -- is unobtainable from source. Readers
reasonably lag the library, so that is the normal case rather than a v6 quirk,
and it is the open half of #724 Phase 0 step 5.

Pass `LONGFELLOW_CIRCUITS_GENERATED=1` alongside `LONGFELLOW_CIRCUITS` when the
directory came from this tool. The tests that need an older revision then skip
with a `NOT COVERED` line naming what was lost, rather than failing over an
absence no machine can fix. It is an environment variable and not a marker file
in the directory on purpose: `Open` would skip a dotfile, but not every reader of
a circuit directory goes through `Open` -- the tests pick a file with `os.ReadDir`
and copy `entries[0]`, so a marker sorting first gets copied AS a circuit.

### Two CI workflows, and why neither replaces the other

`.github/workflows/ci.yml` here and `zk-integration.yml` in irmago both end up
running `build-module.sh`. That looks like duplication and is not: they hold
different halves still.

| | this repo's `ci.yml` | irmago's `zk-integration.yml` |
| --- | --- | --- |
| irmago | the **pinned** one, from `go.mod` | the **commit under test**, via a `replace` made inside the job |
| this module | the commit under test | whatever is on `main` |
| fires on | pushes and PRs here | pushes and PRs there |

One varies this module against a fixed irmago; the other varies irmago against a
fixed module. Drop irmago's and a rename there breaks the prover silently --
`NewHolder` to `GenerateDeviceSigner`, the DC API move into `isomdoc` and
`VerifyZkDocument`'s extra argument were all found weeks late, which is why that
job exists. Drop this one and a change pushed HERE is unverified until some
unrelated irmago commit happens to build it.

Only this workflow runs upstream's own ctest suite (`test-longfellow.sh`), which
carries Google's mdoc vectors. Both build the image, so both catch a patch that
stopped applying -- but only on their own repository's schedule.

The image cache is keyed identically in both (`Dockerfile` plus `patches/*.patch`)
so an entry means the same thing in each. It is NOT shared: Actions caches are
scoped per repository, so the ~20 minute C++ build is paid once in each, then
cached until the pinned ref or the patches move.

### Why the image needed its own Go

Ubuntu 24.04 ships Go 1.22; irmago's `go.mod` declares `go 1.27`, which 1.22
refuses outright rather than degrading. The Dockerfile installs the official
1.27.0 tarball **as a final layer**, so bumping Go never invalidates the twenty
minutes of C++ above it.

---

## Measured

x86_64, in the container, v7/1-attribute circuit:

| | |
|---|---|
| prove | **711 ms**, 360 KB proof |
| verify | **413 ms** |
| `Open`, 8 circuits, cold | **10.0 s** |
| `Open`, 8 circuits, cached | **59 ms** (169x) |
| `Open`, compiled-in `MapCache` | **0 ms** |
| a whole org-iso-mdoc session, request to sealed response | **847 ms** |

The suite is green in the container -- the round trip proves once per circuit
revision held, so the profile's v6 circuit is exercised as well as the newest.
Only `TestProfileWithFileCache` skips, and only when `LONGFELLOW_CACHE` names no
path that persists between runs.

The session figure is the interesting one: it is a complete `isomdoc.Session`
-- reader authentication, consent, narrowing, deviceAuth, the proof, and HPKE
sealing -- so proving is most of it and everything else is noise by comparison.

These are x86_64. arm64 has since been measured too — see "It runs on the device"
below and `memprofile/README.md`, which carries the platform warning and the
patched-vs-unpatched comparison.

---

## Circuits are identified by CONTENT

`circuit_id` reads **no field** of the `ZkSpecStruct` it is handed — verified by
reading `mdoc_circuit_id.cc`, where it is used only in the null check. So the id
depends on the circuit bytes alone, which makes this possible:

```
id   := circuit_id(bytes)          // any non-null spec will do
spec := find_zk_spec(system, id)   // an id in no kZkSpecs entry is refused
```

**The filename is never consulted.** Google's loader requires the file to be
*named* its own hash and silently skips it otherwise; Multipaz parses the hash
out of the filename and never checks it. A test copies a circuit to
`not-a-hash.bin` and it still loads, under its true hash.

A file that is not a circuit **fails the load** rather than being skipped: a
wallet that silently came up with fewer circuits than were installed would fall
back to plain presentation for a reason nobody could see.

This matters because §A.8 requires the relying party to check a circuit's hash
against the scheme owner's accepted set **before** verifying a proof. A proof
under a withdrawn circuit verifies perfectly well, so that check is the only
thing standing between a revoked circuit and an accepted presentation — and it is
theatre unless the hash a circuit is filed under is the hash its bytes have.

### Two version counters, and only one of them is pinned

This trips people up, so it is worth stating flatly.

| | |
|---|---|
| **`longfellow-libzk-v1`** | the ZK **system**. This is what AV Annex A §A.8 pins: "Support for any other Zero-Knowledge Proof system does not constitute conformance with this profile." |
| **5 / 6 / 7** | the **circuit-spec revision** inside that system. A `version` parameter in `ZkSystemSpec`, negotiated per request. |

**§A.8 does not pin the circuit revision.** It pins the system, requires the
relying party to check `circuit_hash` against the scheme owner's published set of
accepted circuits, and leaves that set to be "published and maintained by the
scheme owner separately from this document". Circuit churn does not touch profile
conformance.

So why does everything here say v6? Because of an **observation, not a
requirement**: the captured EUDI AV reader offered **v6 only**, one revision
behind the library, and the scheme owner's accepted set **does not exist yet**.
v6 is what readers ask for today, which is a fact about the deployed ecosystem
that could change without the profile changing at all.

### Circuits cannot be generated here

`generate_circuit` only emits the library's newest version, so asking it for v6
while the library is at v7 returns `CIRCUIT_GENERATION_INVALID_ZK_SPEC_VERSION`.
Every deployment must therefore ship circuits obtained elsewhere.

That is structural rather than a v6 quirk: **any revision the ecosystem is using
that is not the library's newest cannot be regenerated**, and since readers
reasonably lag the library, that is the normal case. It also makes #724's F-Droid
contingency ("pin longfellow to a pre-v7 commit and regenerate") a real fork with
a real cost.

---

## The session layer runs against this module, not a fake

irmago's own session tests fake the prover, because the real one lives in a
module irmago does not link -- that is the point of the split. A fake returns
whatever it was told to, so it cannot disagree with the session about attribute
ordering, circuit selection, or what a prover refuses.

`longfellow/session_test.go` drives `isomdoc.Session` end to end against the
library: the reader offers circuits, the session takes the ZK branch, the native
library proves, the response is sealed, and the reader opens it and verifies
through `mdoc.VerifyZkDocument` -- the relying party's own gate, accepted-circuit
check included. The only thing faked is the user saying yes.

Two cases a fake cannot police:

- **What gets proved is what was disclosed, not what was requested.** The reader
  asks for two elements and the user agrees to one, so the session must pick the
  ONE-attribute circuit. Counting the request instead would select the
  two-attribute circuit and the library would refuse the mismatch. Against a fake
  that off-by-one passes silently.
- **The reader offers only v6** while we hold v7 as well, so the version
  intersection actually decides something. The proof comes back under
  `..._6_1_4096_2945_137e5a75...` -- the revision AV readers actually offer.

### The whole deployment, through the relying party entry points

The flow tests go one step further than the session tests: nothing is
hand-rolled on either side. `mdoc.TestIssuer` mints the credential,
`isomdoc.Session` is the wallet, and `eudi/isomdoc/reader.Builder` is the
verifier -- `Build` makes the signed request, `Open` unseals the answer, and
`Verify` applies the trust model, the accepted-circuit set, the timestamp check
and the proof in the order A.8 requires. Three cases:

- **the happy path**: a real proof, verified under the pinned IACA, carrying
  exactly the one element that was disclosed;
- **a foreign issuer is refused** even though the proof verifies -- a proof
  establishes that SOME key signed the attestation, never whose, so the refusal
  must come from the trust model and this pins that it does;
- **the A.6 fallback**: same loop, wallet without a prover, and the reader
  accepts the plain signed disclosure instead.


### And against a verifier that is not ours at all

Everything above verifies with *our* verifier. A prover and a verifier built from
the same source agree by construction, so the interesting question is whether a
proof this wallet produced satisfies an implementation that shares no code with
it.

`longfellow/interop_live_test.go` answers it against **multipaz-verifier-server**
— Kotlin, its own CBOR, and it rebuilds the session transcript from its own state
rather than trusting ours. Four parties, no faked joins: `mdoc.TestIssuer` issues,
`isomdoc.Session` is the wallet, this module proves, and their server checks.
Their reply:

```
ZK proof         Successfully validated proof 🪄
Namespace        eu.europa.ec.av.1
age_over_18      true
Issuer           Not in trust list (CN=Test Age Verification DS - 001,O=Yivi Test)
```

The test has to live here rather than in irmago for the same reason the rest of
this file does: irmago cannot link a prover, so its own
`eudi/isomdoc/interop_live_test.go` can only round-trip a **plain** presentation.

It **skips** unless a verifier answers, so no automated run covers it: neither
this repository's `ci.yml` nor irmago's `zk-integration.yml` stands one up. Run it by
hand:

```
docker compose --profile interop up --build -d multipaz-verifier   # in irmago

docker run --rm -v "$PWD:/work" -w /work \
  -v "<circuits>:/circuits:ro" \
  -e LONGFELLOW_CIRCUITS=/circuits \
  -e MULTIPAZ_VERIFIER_URL=http://host.docker.internal:8006 \
  --add-host host.docker.internal:host-gateway \
  longfellow-build go test ./longfellow/ -run TestLiveZkRoundTrip -v -count=1
```

Two things it pins that nothing else does:

- **the spec-id convention agrees end to end.** Their request offers
  `longfellow-libzk-v1_6_1_4096_2945_137e5a75…` and their verifier resolves the
  id we echo back by exact string match. Ours is built in `mdoc.specForCircuit`,
  theirs from the circuit's filename; nothing but this compares them.
- **reader authentication does NOT happen, and cannot.** With `signRequest` their
  server signs with `readerAuthAll`, the request-wide COSE_Sign1 from the 2025
  edition of 18013-5; irmago implements the 2021 edition, which has one
  `readerAuth` per `DocRequest` and no request-wide signature. So irmago
  correctly reports the reader unauthenticated and releases nothing, and the test
  discloses from `Requested` and asserts that situation rather than hiding it.
  Reader auth is tracked as separate work; when it lands, those assertions fail
  and the workaround in that file should go.

## The verified-hash cache

Identifying a circuit costs ~1.2 s. Four v6 circuits — what the captured EUDI AV
reader offered — is ~5 s on every launch, for an answer that cannot have changed
unless the files did.

```go
cache := longfellow.OpenFileCache("/path/to/circuits.json")
system, err := longfellow.OpenDir(dir, longfellow.WithCache(cache))
```

The cache maps `FileDigest` → circuit id. `FileDigest` is a plain `sha256` of the
file bytes: pure Go, microseconds, no decompression. The expensive call
establishes once that *these bytes are the circuit with that published id*; the
cheap digest afterwards only has to establish *these are still those bytes*.

> **`FileDigest` is NOT the circuit id.** Different values over the same file —
> the id is SHA-256 over the two *parsed* circuits' own ids, which is why it
> cannot be computed without decompressing. A test pins the distinction.

### Two flavours, and the second is the one to ship

- **`FileCache`** — JSON on disk, written atomically. For circuits that arrive
  after install: downloaded, or provisioned.
- **`MapCache`** — a mapping compiled into the binary, shipping signed with the
  application. `FileCache.Entries()` returns exactly what a build step would emit
  as a literal.

### What a cache is trusted with, which is not nothing

A cache can make a circuit load under an id its bytes do not have, and that id is
what the relying party's accepted-circuit gate compares. **A cache must be
protected exactly as well as the circuit files themselves.** That is the ordinary
case — both live in app-private storage — but it is a real assumption, and it is
why no cache is used unless a caller passes one. `MapCache` carries no such
assumption, which is why it is the recommended form for bundled circuits.

### Three properties, each with a test

1. **Nothing a cache says can make a load fail that would otherwise succeed.** A
   stale entry falls back to full identification and is then corrected in place.
2. **Changed bytes miss**, so a cache cannot vouch for a file it never saw.
3. **A missing, unreadable or unrecognised cache file means "nothing remembered
   yet", never a refusal to start.** An unknown format version is treated as
   empty rather than misread.

---

## Four deliberate departures from Google's binding

`longfellow/binding.go` is adapted from
`reference/verifier-service/server/zk/proofs.go` (Apache-2.0, headers retained).
Each change is commented at its site. **Do not "restore" any of them.**

1. **`run_mdoc_prover` is bound.** The reference service only verifies; a wallet
   is the half that proves, and #724 names this as the new part.
2. **`fill_attribute` refuses over-long input.** Theirs clamps `cbor_value` to 64
   bytes silently, which yields a perfectly valid proof about a value nobody
   requested — the wrong failure mode for a credential.
3. **`circuit_id` is given a real `ZkSpecStruct`.** Theirs passes `malloc`'d
   memory with only `num_attributes` set; the rest is uninitialised.
4. **The package logs nothing.** Theirs prints circuit loading and verification
   progress with the standard logger. The library's OWN logger is also turned
   down to ERROR at init — its default is INFO, at which every prove and verify
   prints timing lines to stderr (logcat on Android). The call goes through
   `set_mdoc_log_level`, which `patches/0001-expose-logger-through-c-api.patch`
   adds to the installed C ABI. Before that patch the only way to reach the
   logger was a C++ shim redeclaring `proofs::set_log_level`, which put a `.cc`
   file in this package and brought the Android link trap below with it.

---

## patches/

Changes submitted upstream but not yet in a ref we can pin. The Dockerfile
applies each one to the pinned checkout before building, so the library is
still built from upstream's own source at a named commit and the delta stays
one reviewable file rather than a fork nobody tracks. A patch that stops
applying after a ref bump fails the image build: that is the signal to check
whether it landed upstream and, if it did, to delete the file and bump the ref.

| Patch | Why it is carried |
| --- | --- |
| `0001-expose-logger-through-c-api.patch` | Adds `set_mdoc_log_level` to the C ABI, so the library's logger can be turned down without a C++ translation unit in this package. **No Go package here contains C++ source, and that is a property worth keeping** — see the first trap below for what it costs to lose it. |
| `0002-reserve-the-circuits-actual-size.patch` | Sizes the decompression buffer from the zstd frame header instead of `kCircuitSizeMax`. The bound is 130 MB and `std::vector` value-initialises, so every byte of it was being touched on every call. |
| `0003-free-the-verifiers-circuit-early.patch` | Scopes the verifier's decompressed circuit the way the prover already scopes its own, instead of holding it for the whole verification. |

**0002 and 0003 are the memory fixes, and until 2026-10-09 they did not ship.**
They existed only as `replace.pl` blocks in `memprofile/`, applied by two
measurement scripts and by nothing on the path that builds the libraries a
wallet links. The A/B that justified them is in
`androidbench/results/cold-runs.csv`: with the v7 circuit the wallet actually
uses, `baseline` peaks at **215.2 MB** and `patched` at **165.0 MB**. Moving
them here is what makes the shipped library the measured one.

---

## Known gaps

- **The sumcheck is 34% of prove and is not ours to change.** Profiled on device
  with `simpleperf`: `ProverLayers::layer` and its `Quad`/`Eqs` helpers dominate,
  and they are the GKR prover — the cryptography itself.
- **Circuit parsing costs more than the cryptography's commitment scheme.**
  `ApproximateDeltaTableBuilder::dedup` alone is 306 ms, four times all of
  Ligero, and it is **algorithmic rather than cache-bound**: IPC 3.31 near the
  A78's ceiling with a 0.23% miss rate, executing 16% of every instruction in the
  run. That is the one optimisation worth proposing upstream besides the memory
  patches.
---

## Cross-compiling for Android

**No device is required.** This produces artefacts and proves they link; running
them on a phone is a separate exercise.

```
docker build -t longfellow-build .                            # x86_64 first
docker build -t longfellow-android -f Dockerfile.android .    # adds NDK + arm64 build

docker run --rm \
  -v D:\Yivi\longfellow-go:/work/longfellow-go \
  -w /work/longfellow-go longfellow-android build-module-android.sh
```

Result, measured:

| | |
|---|---|
| `libmdoc_static.a` | `elf64-littleaarch64`, all six C symbols exported |
| the Go module | builds **and links** for `GOOS=android GOARCH=arm64` |
| linked test binary | ELF64 / AArch64, 22.7 MB (22,689,120 bytes) |
| its `DT_NEEDED` | `liblog`, `libdl`, `libc` — **no `libc++_shared`** |

`build-longfellow-android.sh` is adapted from upstream's own `android.sh`, with
four changes: the NDK toolchain path is `linux-x86_64` rather than
`darwin-x86_64` (theirs is written for a Mac); `PATH` is exported around the
OpenSSL build rather than prefixed onto `./Configure` alone; the dependency
sources are **pinned** to tags instead of cloning master; and `CMAKE_PREFIX_PATH`
is passed alongside their `CMAKE_FIND_ROOT_PATH`, since the Android toolchain
sets `CMAKE_FIND_ROOT_PATH_MODE_PACKAGE` to `ONLY`.

### Three traps worth not rediscovering

**A C++ source file in this package changes how the whole thing links.** Go
links any package containing a `.cc` file with the C++ driver rather than the C
one, and the NDK's `clang++` then links **libc++_shared by default** — writing a
`DT_NEEDED` on `libc++_shared.so` into `libgojni.so`, a library nothing packages
into an APK. `-lc++_static` does not prevent this: it adds archives, it does not
stop the driver adding the shared runtime. Hence `-static-libstdc++` beside it;
the two look redundant and are not. This cost an afternoon when `quiet.cc`
landed: the wallet died at startup with `UnsatisfiedLinkError ... libc++_shared.so`
and a stack trace naming `Seq.<clinit>` and nothing about C++, while
`bind_go.sh`, `go vet` and this module's whole test suite all passed.

That file is gone — the logger is reached through the C ABI now, so the C
driver is in use and `-static-libstdc++` is not currently doing any work. It
stays as a guard, because the archives do not prevent the failure on their own
and the day a `.cc` returns is not the day to rediscover that. **Adding any C++
source to a Go package here re-arms the trap.** Verify at the binary level
after touching anything in the cgo package:

    readelf -d <aar>/jni/arm64-v8a/libgojni.so | grep NEEDED
    # liblog, libandroid, libm, libdl, libc.  libc++_shared = broken.

**googletest and benchmark must be built even though nothing a wallet links
needs them.** `CMake/proofs.cmake` calls `find_package` for both at CONFIGURE
time, unconditionally, so the project will not configure without them even when
the only target requested is `libmdoc_static`. Skipping them was tried; it fails
at configure. Upstream builds them for that reason, not by oversight.

**The NDK has no libstdc++.** It ships LLVM's libc++, so the cgo directives
select `-lc++_static -lc++abi` for Android. The constraint is written
`#cgo linux,!android` on the libstdc++ line, and the negation is load-bearing:
Go treats `GOOS=android` as satisfying the `linux` build constraint too, so a
plain `#cgo linux` would put `-lstdc++` back on the Android link line.

### It runs on the device, too

`androidbench/` drives it: `20/20 tests pass` on a Dimensity 8100, including a
whole `isomdoc` session. Measured there, interleaved:

| v6 / 1 attr | prove | verify | peak RSS |
|---|---|---|---|
| unpatched library | 2074 ms | 1050 ms | 211.0 MB |
| patched library | 2004 ms | 997 ms | **139.0 MB** |

#724's gate — *"~8 s or OOMs on a 2022 device"* — passes with room to spare.

### Benchmark with benchstat, not with print statements

`longfellow/bench_test.go` has `BenchmarkProve`, `BenchmarkVerify` and
`BenchmarkAdapterOnly`. Use them rather than timing loops:

```
./longfellow.test -test.bench=. -test.benchtime=3x -test.count=10 -test.run='^$' > new.txt
go install golang.org/x/perf/cmd/benchstat@latest
benchstat old.txt new.txt
```

`benchstat` reports a delta with a p-value and prints `~` when a difference is
not significant — which is the question a printed millisecond cannot answer, and
on a thermally throttling phone that question has teeth. A 533 ms "regression"
was investigated here and turned out to be the device cooling between two runs
taken hours apart. **Interleave variants; never compare across sessions.**

`BenchmarkAdapterOnly` answers the recurring "how much does our Go layer cost?"
directly, by giving the adapter a prover that returns immediately: **77–138 µs**
against a ~2000 ms prove. Everything this module does outside the native call is
noise.

### What is still missing

- **iOS.** Upstream has an `ios.sh` and `lib/CMakeLists.txt` has an iOS branch
  expecting `deps-ios/`, so the recipe exists — but the iOS SDK ships only inside
  Xcode, which is macOS-only and not redistributable. It is licence-blocked here,
  not technically impossible: clang can target `arm64-apple-ios` perfectly well.
- **A true end-to-end comparison against Multipaz's app.** Their harness would
  have to be rebuilt (`androidInstrumentedTest` + Gradle). What we have instead
  is their library in our harness, which is the controlled half of the question —
  see `memprofile/README.md`.

---

## Where the numbers and the history live

- `memprofile/README.md` — peak RSS, the two upstream patches, the
  `kCircuitSizeMax` archaeology, and the platform warning.
- `scripts/build-longfellow.sh` — the from-source library build.
- `scripts/test-longfellow.sh` — upstream's own suite. Scale `JOBS` to memory,
  not cores: two ZK tests together hold 2.2 GB, so a high `-j` gets OOM-killed
  rather than failing an assertion.

---

## Shipping the native libraries to a wallet

irmamobile links native dependencies per ABI, because "gomobile doesn't support
per-ABI CGO flags and the linker rejects `.a` files for the wrong architecture" —
so `bind_go.sh` invokes `gomobile bind` once per ABI with different `-I`/`-L` and
merges the resulting `libgojni.so` files into one AAR. It already does exactly
this for SQLCipher, which it downloads as a release tarball and verifies by
SHA-256.

`scripts/package-android-libs.sh` produces the same shape:

```
longfellow-<commit>-p<patchset>-android.tar.gz
  android/include/…           headers, shared across ABIs
  android/<abi>/lib/*.a       arm64-v8a · armeabi-v7a · x86_64
  android/MANIFEST.txt        the commit, and every patch applied over it
```

so adding longfellow to a wallet build is a second set of flags on invocations
that already exist, not new machinery — which is what irmamobile's `bind_go.sh`
now does, pinning `LONGFELLOW_VERSION` and the published `.sha256` beside the
SQLCipher pair it already carried.

The name is built from longfellow's pinned commit rather than a version we
invent, plus an eight-character digest of `patches/` when there are any: what a
consumer needs to know is which source it came from, and `patches/` is applied
with `git apply`, so a patched checkout still reports the pinned ref and two
builds of one commit would otherwise be indistinguishable. The `-p<patchset>`
half disappears once `patches/` is empty, which is the state this returns to as
each patch lands upstream.

### Where the tarball lives, and why that is not a repo

**Release assets on this repository** — not committed to it, and not in the
module graph. #724's constraint 1 forbids a prebuilt binary entering either, and
a release asset is neither: `go get` sees only the source zip, and nothing in
`git` holds a `.a`.

The build recipe and its output stay together, which is the argument for this
over a separate `yivi-longfellow-prebuilt` repo whose only job would be to run a
script that lives here.

### It does not remove the need for a source build

F-Droid requires building from source and will not accept a downloaded binary.
That build runs `package-android-libs.sh` itself instead of fetching its output —
which is the same script, so there is one recipe rather than two that can drift.
The prebuilt exists to save ordinary CI and developers the ~20 minutes per ABI of
compiling OpenSSL, zstd and longfellow, not to be the only way in.

---

## License

Apache License 2.0 — see `LICENSE`. `longfellow/binding.go` is adapted from
google/longfellow-zk's reference verifier service (Apache-2.0, headers
retained); the library itself is built from source at the pinned commit and
linked statically, never vendored.
