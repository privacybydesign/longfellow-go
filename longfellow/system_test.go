package longfellow_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/privacybydesign/irmago/eudi/credentials/mdoc"
	"github.com/privacybydesign/irmago/eudi/credentials/mdoc/zk"
	"github.com/privacybydesign/irmago/eudi/isomdoc"
	"github.com/privacybydesign/longfellow-go/longfellow"
	"github.com/stretchr/testify/require"
)

// Circuits are assets this module cannot generate — generate_circuit only emits
// the library's newest version, while readers ask for older revisions -- so the
// tests need a directory of them. They are not vendored here: #724 forbids prebuilt binaries
// in the module graph, and a circuit is a build artefact of the same library.
const circuitDirEnv = "LONGFELLOW_CIRCUITS"

func circuitDir(t testing.TB) string {
	t.Helper()
	dir := os.Getenv(circuitDirEnv)
	if dir == "" {
		t.Skipf("set %s to a directory of longfellow circuits to run this", circuitDirEnv)
	}
	return dir
}

func openSystem(t testing.TB) *longfellow.System {
	t.Helper()
	system, err := longfellow.OpenDir(circuitDir(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, system.Close()) })
	return system
}

// ============================================================
// Loading
// ============================================================

// Every circuit is identified by recomputing circuit_id over its own bytes and
// looking the result up in the library's table. Nothing here consults a
// filename, which is the difference from both references: Google's loader
// requires the file to be named its own hash, and Multipaz parses the hash out
// of the filename and never checks it.
func TestOpenIdentifiesCircuitsByContent(t *testing.T) {
	system := openSystem(t)

	circuits := system.Circuits()
	require.NotEmpty(t, circuits)

	for _, circuit := range circuits {
		require.Equal(t, "longfellow-libzk-v1", circuit.System)
		require.Len(t, circuit.Hash, 64, "circuit_id is a SHA-256 in hex")
		require.Positive(t, circuit.Version)
		require.Positive(t, circuit.NumAttributes)
		require.Positive(t, circuit.BlockEncHash)
		require.Positive(t, circuit.BlockEncSig)
	}

	// Newest version first, then by attribute count: a stable order, so what a
	// reader is offered does not depend on map iteration.
	for i := 1; i < len(circuits); i++ {
		previous, current := circuits[i-1], circuits[i]
		if previous.Version == current.Version {
			require.LessOrEqual(t, previous.NumAttributes, current.NumAttributes)
			continue
		}
		require.Greater(t, previous.Version, current.Version)
	}
}

// The filename carries no authority. A circuit copied under a misleading name
// still loads, under the hash its bytes actually have.
func TestOpenIgnoresFilenames(t *testing.T) {
	source := circuitDir(t)
	entries, err := os.ReadDir(source)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	content, err := os.ReadFile(filepath.Join(source, entries[0].Name()))
	require.NoError(t, err)

	staging := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(staging, "not-a-hash.bin"), content, 0o644))

	system, err := longfellow.OpenDir(staging)
	require.NoError(t, err)
	defer system.Close()

	circuits := system.Circuits()
	require.Len(t, circuits, 1)
	require.NotEqual(t, "not-a-hash.bin", circuits[0].Hash)
	require.Len(t, circuits[0].Hash, 64)
}

// A file that is not a circuit fails the load rather than being skipped. A
// wallet that silently came up with fewer circuits than were installed would
// fall back to plain presentation for a reason nobody could see.
func TestOpenRefusesBytesThatAreNotACircuit(t *testing.T) {
	circuitDir(t) // skip when circuits are unavailable, for consistency

	staging := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(staging, "rubbish"), []byte("not a circuit"), 0o644))

	_, err := longfellow.OpenDir(staging)
	require.Error(t, err)
}

func TestOpenReportsAnEmptyDirectory(t *testing.T) {
	_, err := longfellow.OpenDir(t.TempDir())
	require.ErrorContains(t, err, "no circuits")
}

// ============================================================
// The whole stack, in one process
// ============================================================

// dcapiTranscript builds a DC API session transcript, the transport the AV
// profile mandates. The nonce is what makes two of them different, which is
// what the replay test turns on.
func dcapiTranscript(t testing.TB, nonce string) mdoc.SessionTranscript {
	t.Helper()

	readerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	encryptionInfo, err := isomdoc.NewDCAPIEncryptionInfo([]byte(nonce), &readerKey.PublicKey)
	require.NoError(t, err)

	encoded, err := cbor.Marshal(encryptionInfo)
	require.NoError(t, err)

	transcript, err := mdoc.NewDCAPISessionTranscript(
		base64.RawURLEncoding.EncodeToString(encoded), "https://verifier.example.com")
	require.NoError(t, err)
	return transcript
}

// mintProvableDocument produces what a wallet would have at the moment it
// decides to prove: a credential it holds, selectively disclosed, with a device
// signature over this session's transcript already attached.
//
// The ordinary presentation path has to run first. The prover refuses a document
// without a deviceSigned, because one of §A.8's statements is about that
// signature — so the proof is taken over the result of the normal flow rather
// than instead of it.
func mintProvableDocument(t testing.TB) (*mdoc.MDoc, mdoc.SessionTranscript) {
	t.Helper()

	const docType = "eu.europa.ec.av.1"
	const namespace = "eu.europa.ec.av.1"

	issuer, err := mdoc.NewTestIssuer()
	require.NoError(t, err)
	holder, err := mdoc.GenerateDeviceSigner()
	require.NoError(t, err)

	credential, err := issuer.Issue(docType, namespace,
		map[string]any{"age_over_18": true, "age_over_21": false}, holder.PublicKey())
	require.NoError(t, err)

	presented, err := mdoc.SelectiveDisclose(credential, namespace, []string{"age_over_18"})
	require.NoError(t, err)

	transcript := dcapiTranscript(t, "a nonce for this session..")

	deviceAuth, err := holder.SignDeviceAuth(docType, transcript)
	require.NoError(t, err)

	withDeviceSigned, err := mdoc.AttachDeviceSigned(presented, deviceAuth)
	require.NoError(t, err)

	return withDeviceSigned, transcript
}

// This is what Phase 0 needed three programs, a JSON file and a docker mount to
// do. irmago mints the credential, mdoc.ProverSystem marshals it across the
// boundary, this module proves it, and the proof verifies — all in one process,
// with no file ever touching disk.
func TestProveAndVerifyRoundTripThroughTheAdapter(t *testing.T) {
	system := openSystem(t)
	prover := mdoc.NewProverSystem(system)

	document, transcript := mintProvableDocument(t)

	spec, ok := prover.MatchingSpec(prover.SystemSpecs(), 1)
	require.True(t, ok, "a one-attribute circuit must be available")

	start := time.Now()
	zkDocument, err := prover.GenerateProof(spec, *document, transcript, time.Now())
	require.NoError(t, err)
	t.Logf("proved in %v, %d byte proof, circuit %s",
		time.Since(start).Round(time.Millisecond), len(zkDocument.Proof), spec.ID)

	require.NotEmpty(t, zkDocument.Proof)
	require.Equal(t, spec.ID, zkDocument.DocumentData.ZkSystemSpecID)
	require.Equal(t, "eu.europa.ec.av.1", zkDocument.DocumentData.DocType)

	start = time.Now()
	require.NoError(t, prover.VerifyProof(*zkDocument, spec, transcript))
	t.Logf("verified in %v", time.Since(start).Round(time.Millisecond))
}

// A verifier that accepts anything proves nothing.
func TestATamperedProofIsRejected(t *testing.T) {
	system := openSystem(t)
	prover := mdoc.NewProverSystem(system)

	document, transcript := mintProvableDocument(t)
	spec, ok := prover.MatchingSpec(prover.SystemSpecs(), 1)
	require.True(t, ok)

	zkDocument, err := prover.GenerateProof(spec, *document, transcript, time.Now())
	require.NoError(t, err)

	zkDocument.Proof[len(zkDocument.Proof)/2] ^= 0xff
	require.Error(t, prover.VerifyProof(*zkDocument, spec, transcript))
}

// The proof binds to the session it was made for. Replaying it into another
// session must fail, which is the property the transcript is there to provide.
func TestAProofDoesNotVerifyAgainstAnotherTranscript(t *testing.T) {
	system := openSystem(t)
	prover := mdoc.NewProverSystem(system)

	document, transcript := mintProvableDocument(t)
	spec, ok := prover.MatchingSpec(prover.SystemSpecs(), 1)
	require.True(t, ok)

	zkDocument, err := prover.GenerateProof(spec, *document, transcript, time.Now())
	require.NoError(t, err)

	other := dcapiTranscript(t, "a different session entirely")

	require.Error(t, prover.VerifyProof(*zkDocument, spec, other))
}

// ============================================================
// The interface contract
// ============================================================

// A circuit the system does not hold is the one failure a caller is meant to
// treat as a fallback rather than a fault, so it has to be distinguishable.
func TestProveReportsErrNoCircuit(t *testing.T) {
	system := openSystem(t)

	_, err := system.Prove(zk.ProofRequest{
		Circuit:        "0000000000000000000000000000000000000000000000000000000000000000",
		DocType:        "eu.europa.ec.av.1",
		DeviceResponse: []byte{0x01},
		IssuerKeyX:     "0x" + "aa" + "00000000000000000000000000000000000000000000000000000000000000"[:62],
		IssuerKeyY:     "0x" + "bb" + "00000000000000000000000000000000000000000000000000000000000000"[:62],
		Transcript:     []byte{0x02},
		Attributes: []zk.Attribute{
			{Namespace: "eu.europa.ec.av.1", Identifier: "age_over_18", Value: []byte{0xf5}},
		},
		Timestamp: time.Now(),
	})
	require.ErrorIs(t, err, zk.ErrNoCircuit)
}

// The attribute count is part of a circuit's identity, so a mismatch is caught
// here rather than surfacing as an opaque native code.
func TestProveRefusesTheWrongAttributeCount(t *testing.T) {
	system := openSystem(t)

	var oneAttribute zk.Circuit
	for _, circuit := range system.Circuits() {
		if circuit.NumAttributes == 1 {
			oneAttribute = circuit
			break
		}
	}
	require.NotEmpty(t, oneAttribute.Hash, "a one-attribute circuit must be available")

	value := []byte{0xf5}
	_, err := system.Prove(zk.ProofRequest{
		Circuit:        oneAttribute.Hash,
		DocType:        "eu.europa.ec.av.1",
		DeviceResponse: []byte{0x01},
		IssuerKeyX:     "0x" + "aa00000000000000000000000000000000000000000000000000000000000000",
		IssuerKeyY:     "0x" + "bb00000000000000000000000000000000000000000000000000000000000000",
		Transcript:     []byte{0x02},
		Attributes: []zk.Attribute{
			{Namespace: "eu.europa.ec.av.1", Identifier: "age_over_18", Value: value},
			{Namespace: "eu.europa.ec.av.1", Identifier: "age_over_21", Value: value},
		},
		Timestamp: time.Now(),
	})
	require.ErrorContains(t, err, "opens 1 attributes")
}

func TestClosedSystemRefusesWork(t *testing.T) {
	system, err := longfellow.OpenDir(circuitDir(t))
	require.NoError(t, err)
	require.NoError(t, system.Close())

	_, err = system.Prove(zk.ProofRequest{Circuit: "whatever"})
	require.Error(t, err)
}
