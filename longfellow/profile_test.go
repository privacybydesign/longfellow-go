package longfellow_test

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/privacybydesign/irmago/eudi/credentials/mdoc"
	"github.com/privacybydesign/irmago/eudi/credentials/mdoc/zk"
	"github.com/privacybydesign/longfellow-go/longfellow"
	"github.com/stretchr/testify/require"
)

// ============================================================
// WHERE THE GO STACK'S EXTRA TIME GOES -- AND WHY IT TURNED OUT TO BE NOWHERE
// ============================================================
//
// ANSWERED, 2026-09-22, and kept because the question keeps coming back.
//
// A bare C++ probe appeared to prove in 1490 ms against this module's 2023 ms
// over the same circuit and the same library, and 533 ms went looking for an
// explanation. There was none to find: the two figures came from different
// sessions, and the phone had warmed up in between. Re-running the C++ probe
// immediately gave 2342 ms -- the same binary, 57% slower, purely thermal.
//
// Measured properly -- interleaved, and with the native call timed exactly --
// the adapter costs 77-138 microseconds against a ~2000 ms prove. Everything
// this module does outside the native call is noise.
//
// The technique is still worth keeping: mdoc.NewProverSystem takes a zk.System
// interface, so a decorator times the native call with no change to any
// production type, and adapter time is total minus native. prove_adapter has
// read 0 ms on every run since.

// timedSystem records how long the wrapped system spends in Prove and Verify.
type timedSystem struct {
	inner zk.System

	proveNative  time.Duration
	verifyNative time.Duration
}

func (t *timedSystem) Name() string           { return t.inner.Name() }
func (t *timedSystem) Circuits() []zk.Circuit { return t.inner.Circuits() }

func (t *timedSystem) Prove(request zk.ProofRequest) ([]byte, error) {
	start := time.Now()
	proof, err := t.inner.Prove(request)
	t.proveNative = time.Since(start)
	return proof, err
}

func (t *timedSystem) Verify(request zk.VerificationRequest) error {
	start := time.Now()
	err := t.inner.Verify(request)
	t.verifyNative = time.Since(start)
	return err
}

// TestProfileTheAdapterGap reports the split. Run it cold — a fresh process per
// run — because the gap was measured cold and a warm process hides it.
//
//	./longfellow.test -test.run TestProfileTheAdapterGap -test.v
func TestProfileTheAdapterGap(t *testing.T) {
	native := openSystem(t)
	timed := &timedSystem{inner: native}
	prover := mdoc.NewProverSystem(timed)

	document, transcript := mintProvableDocument(t)
	spec, ok := prover.MatchingSpec(prover.SystemSpecs(), 1)
	require.True(t, ok)

	// What the runtime looks like before the expensive part, so a reader can see
	// whether the heap or the thread count is a plausible suspect.
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	totalStart := time.Now()
	zkDocument, err := prover.GenerateProof(spec, *document, transcript, time.Now())
	proveTotal := time.Since(totalStart)
	require.NoError(t, err)

	verifyStart := time.Now()
	require.NoError(t, prover.VerifyProof(*zkDocument, spec, transcript))
	verifyTotal := time.Since(verifyStart)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	proveAdapter := proveTotal - timed.proveNative
	verifyAdapter := verifyTotal - timed.verifyNative

	t.Logf("PROFILE prove_total=%d prove_native=%d prove_adapter=%d",
		proveTotal.Milliseconds(), timed.proveNative.Milliseconds(), proveAdapter.Milliseconds())
	t.Logf("PROFILE verify_total=%d verify_native=%d verify_adapter=%d",
		verifyTotal.Milliseconds(), timed.verifyNative.Milliseconds(), verifyAdapter.Milliseconds())
	t.Logf("PROFILE gc_cycles=%d gc_pause_us=%d heap_mb=%d threads=%d cpus=%d",
		after.NumGC-before.NumGC,
		(after.PauseTotalNs-before.PauseTotalNs)/1000,
		after.HeapAlloc/(1024*1024),
		threadCount(), runtime.NumCPU())
}

// TestProfileWithFileCache isolates a confound in the comparison against
// Multipaz, and it is the reason this test exists separately from the one above.
//
// Our Open recomputes circuit_id over every circuit, which decompresses and
// parses ~88 MB before a proof is ever requested. Neither of the things we
// compare against does that: the bare C++ probe goes straight to
// run_mdoc_prover, and Multipaz's addCircuit reads the hash out of the FILENAME
// and verifies nothing at all.
//
// So the Go process enters the prove with a very different memory history, and
// that history — not the adapter — may be where the gap lives.
//
// The experiment: point LONGFELLOW_CACHE at a path that survives between runs.
// The first process populates it and pays circuit_id; the second finds the file
// digest already recorded and skips it entirely. If the prove is faster in the
// second process, the cost was the aftermath of that 88 MB, not our code.
//
//	adb shell rm -f /data/local/tmp/lfbench/circuits.json   # first: cold
//	LONGFELLOW_CACHE=/data/local/tmp/lfbench/circuits.json ./longfellow.test -test.run TestProfileWithFileCache -test.v
//	# then run it a second time without removing the cache
func TestProfileWithFileCache(t *testing.T) {
	cachePath := os.Getenv("LONGFELLOW_CACHE")
	if cachePath == "" {
		t.Skip("set LONGFELLOW_CACHE to a path that persists between runs")
	}

	cache := longfellow.OpenFileCache(cachePath)
	primed := len(cache.Entries())

	openStart := time.Now()
	native, err := longfellow.OpenDir(circuitDir(t), longfellow.WithCache(cache))
	openTook := time.Since(openStart)
	require.NoError(t, err)
	defer native.Close()

	timed := &timedSystem{inner: native}
	prover := mdoc.NewProverSystem(timed)

	document, transcript := mintProvableDocument(t)
	spec, ok := prover.MatchingSpec(prover.SystemSpecs(), 1)
	require.True(t, ok)

	start := time.Now()
	_, err = prover.GenerateProof(spec, *document, transcript, time.Now())
	total := time.Since(start)
	require.NoError(t, err)

	t.Logf("PROFILE cache_entries_at_start=%d open_ms=%d circuit_id_ran=%t",
		primed, openTook.Milliseconds(), primed == 0)
	t.Logf("PROFILE prove_total=%d prove_native=%d prove_adapter=%d",
		total.Milliseconds(), timed.proveNative.Milliseconds(),
		(total - timed.proveNative).Milliseconds())
}

// TestProfileSecondProveInSameProcess asks whether the cost is once-per-process
// or once-per-proof.
//
// It matters for a wallet: a per-process cost is paid at the first presentation
// and never again, while a per-proof cost is paid every time the user proves
// their age. The native call re-decompresses the circuit either way, so only the
// Go-side share can differ.
func TestProfileSecondProveInSameProcess(t *testing.T) {
	native := openSystem(t)
	timed := &timedSystem{inner: native}
	prover := mdoc.NewProverSystem(timed)

	spec, ok := prover.MatchingSpec(prover.SystemSpecs(), 1)
	require.True(t, ok)

	for attempt := 1; attempt <= 2; attempt++ {
		document, transcript := mintProvableDocument(t)

		start := time.Now()
		_, err := prover.GenerateProof(spec, *document, transcript, time.Now())
		total := time.Since(start)
		require.NoError(t, err)

		t.Logf("PROFILE attempt=%d prove_total=%d prove_native=%d prove_adapter=%d",
			attempt, total.Milliseconds(), timed.proveNative.Milliseconds(),
			(total - timed.proveNative).Milliseconds())
	}
}

// threadCount reads how many OS threads the process holds. The cgo call blocks
// one of them, and on a big.LITTLE phone which core that thread lands on is a
// live hypothesis for the gap.
func threadCount() int {
	entries, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return -1
	}
	return len(entries)
}
