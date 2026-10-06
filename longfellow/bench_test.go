package longfellow_test

import (
	"testing"
	"time"

	"github.com/privacybydesign/irmago/eudi/credentials/mdoc"
	"github.com/privacybydesign/irmago/eudi/credentials/mdoc/zk"
	"github.com/stretchr/testify/require"
)

// ============================================================
// REAL BENCHMARKS — so the numbers can be compared with benchstat
// ============================================================
//
// These replace hand-rolled timing loops, which is not a style preference. A
// loop that prints milliseconds gives no way to answer the only question that
// matters in a benchmark — "is this difference real, or is it noise?" — and on a
// thermally throttling phone that question has teeth. A 533 ms "regression"
// measured here turned out to be the device cooling down between two runs taken
// an hour apart.
//
// The tooling that fixes it:
//
//	# on the device, several samples so there is a distribution to reason about
//	./longfellow.test -test.bench=. -test.benchtime=5x -test.count=10 \
//	    -test.run='^$' > new.txt
//
//	# on the host, with the equivalent file from the other build
//	go install golang.org/x/perf/cmd/benchstat@latest
//	benchstat old.txt new.txt
//
// benchstat reports the delta WITH a p-value and a confidence interval, and
// prints "~" when the difference is not statistically significant. That is the
// part a print statement cannot do.
//
// Interleave A and B rather than running all of A then all of B. -test.count=10
// interleaves nothing by itself, so for a cross-binary comparison alternate the
// two binaries and concatenate.

// instantSystem answers Prove and Verify immediately, so a benchmark can time
// the adapter with the library taken out of the way. Circuits come from a real
// system, so MatchingSpec still has something honest to choose from.
type instantSystem struct{ inner zk.System }

func (i *instantSystem) Name() string           { return i.inner.Name() }
func (i *instantSystem) Circuits() []zk.Circuit { return i.inner.Circuits() }
func (i *instantSystem) Prove(zk.ProofRequest) ([]byte, error) {
	return []byte("not a proof, and never verified"), nil
}
func (i *instantSystem) Verify(zk.VerificationRequest) error { return nil }

func benchmarkSetup(b *testing.B) (*mdoc.ProverSystem, mdoc.ZkSystemSpec, mdoc.MDoc, mdoc.SessionTranscript) {
	b.Helper()

	system := openSystem(b)
	prover := mdoc.NewProverSystem(system)

	spec, ok := prover.MatchingSpec(prover.SystemSpecs(), 1)
	require.True(b, ok)

	document, transcript := mintProvableDocument(b)
	return prover, spec, *document, transcript
}

// BenchmarkProve measures one full GenerateProof: the adapter plus the native
// call. Compare against BenchmarkProveNative to see the adapter's share.
func BenchmarkProve(b *testing.B) {
	prover, spec, document, transcript := benchmarkSetup(b)

	b.ResetTimer()
	for b.Loop() {
		if _, err := prover.GenerateProof(spec, document, transcript, time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkVerify measures one VerifyProof. The proof is produced outside the
// timed loop: proving is slower than verifying and would dominate.
func BenchmarkVerify(b *testing.B) {
	prover, spec, document, transcript := benchmarkSetup(b)

	zkDocument, err := prover.GenerateProof(spec, document, transcript, time.Now())
	require.NoError(b, err)

	b.ResetTimer()
	for b.Loop() {
		if err := prover.VerifyProof(*zkDocument, spec, transcript); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAdapterOnly measures everything GenerateProof does EXCEPT the native
// call, by giving the adapter a prover that returns immediately.
//
// This is the honest way to size our own overhead: the difference between
// BenchmarkProve and this is the library, and this figure alone is what our code
// costs. Timing the whole thing and subtracting a number from another session is
// how the 533 ms ghost appeared.
func BenchmarkAdapterOnly(b *testing.B) {
	real := openSystem(b)

	// Same circuits, so MatchingSpec behaves identically; Prove returns at once.
	prover := mdoc.NewProverSystem(&instantSystem{inner: real})

	spec, ok := prover.MatchingSpec(prover.SystemSpecs(), 1)
	require.True(b, ok)
	documentPtr, transcript := mintProvableDocument(b)
	document := *documentPtr

	b.ResetTimer()
	for b.Loop() {
		if _, err := prover.GenerateProof(spec, document, transcript, time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}
