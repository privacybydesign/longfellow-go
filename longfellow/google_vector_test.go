package longfellow_test

import (
	"bufio"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/privacybydesign/irmago/eudi/credentials/mdoc/zk"
	"github.com/privacybydesign/longfellow-go/longfellow"
	"github.com/stretchr/testify/require"
)

// Google's own mdoc test vector, driven through this binding.
//
// irmago#724 Phase 1 gates on two things: that a proof generated here verifies
// under Google's reference verifier, and that one of Google's vectors verifies
// here. The first was done months ago. The second was being read as satisfied
// by Multipaz's vector, which is a different implementation answering a
// different question — valuable, and not what the gate says.
//
// # What upstream actually ships, and what that limits this to
//
// mdoc_examples.h carries no proof. Each entry is an issuer public key, a
// session transcript, a timestamp, a docType and a DeviceResponse; the proofs
// in upstream's suite are generated at test time. So "Google's vector verifies
// here" can only mean what it means below: their credential bytes, their
// transcript, their issuer key, their timestamp, proved and verified through
// this module. A test that verified a Google-PRODUCED proof would be stronger
// and is not available to write.
//
// What it does establish is the thing a round trip against our own fixtures
// cannot. Every other proving test in this package proves over a document this
// repository built, so a misreading of the mdoc structure that is consistent
// between our builder and the prover passes everywhere and is invisible. This
// document was built by someone else, to the same specification, and the
// prover's own error codes are specific about structure —
// DOCUMENT_0_MISSING, ISSUER_AUTH_MISSING, MSO_MISSING, DEVICE_SIGNED_MISSING,
// ATTRIBUTE_NOT_FOUND. If our reading of a DeviceResponse had drifted from
// theirs, this is where it shows.
//
// # Keeping the fixture honest
//
// testdata/google_mdoc_vector_0.txt was not transcribed. testdata/dump_vector.cc
// includes mdoc_examples.h and prints entry 0, and was compiled and run against
// the pinned checkout inside the build image, so the bytes are the ones
// upstream's own mdoc_zk_test.cc drives through its one_claim case. Hex text
// rather than a binary fixture on purpose: this repository forces LF on
// committed text, and a CBOR fixture that gets line-ending-normalised is
// corrupt in a way that passes locally and fails for every clone.
func TestGoogleMdocVectorProvesAndVerifies(t *testing.T) {
	vector := loadGoogleVector(t)
	system := openSystem(t)
	defer system.Close()

	// Their vector opens one attribute, so the circuit must be a 1-attribute
	// one. Which revision does not matter here: the vector is a credential,
	// not a circuit, and any revision this build holds proves over it.
	circuit, ok := circuitForAttributes(system, 1)
	if !ok {
		t.Skipf("no 1-attribute circuit in %s", circuitDir(t))
	}

	proof, err := system.Prove(zk.ProofRequest{
		Circuit:        circuit.Hash,
		DocType:        vector.docType,
		DeviceResponse: vector.deviceResponse,
		IssuerKeyX:     vector.issuerKeyX,
		IssuerKeyY:     vector.issuerKeyY,
		Transcript:     vector.transcript,
		Attributes:     vector.attributes,
		Timestamp:      vector.now,
	})
	require.NoError(t, err, "proving over Google's own vector")
	require.NotEmpty(t, proof)

	require.NoError(t, system.Verify(zk.VerificationRequest{
		Circuit:    circuit.Hash,
		DocType:    vector.docType,
		IssuerKeyX: vector.issuerKeyX,
		IssuerKeyY: vector.issuerKeyY,
		Transcript: vector.transcript,
		Attributes: vector.attributes,
		Proof:      proof,
		Timestamp:  vector.now,
	}), "verifying a proof over Google's own vector")
}

// A proof over their vector must not verify against a claim their credential
// does not carry. Without this the test above would pass just as well against a
// verifier that checked nothing about the attribute.
func TestGoogleMdocVectorRefusesAnUnprovenClaim(t *testing.T) {
	vector := loadGoogleVector(t)
	system := openSystem(t)
	defer system.Close()

	circuit, ok := circuitForAttributes(system, 1)
	if !ok {
		t.Skipf("no 1-attribute circuit in %s", circuitDir(t))
	}

	proof, err := system.Prove(zk.ProofRequest{
		Circuit:        circuit.Hash,
		DocType:        vector.docType,
		DeviceResponse: vector.deviceResponse,
		IssuerKeyX:     vector.issuerKeyX,
		IssuerKeyY:     vector.issuerKeyY,
		Transcript:     vector.transcript,
		Attributes:     vector.attributes,
		Timestamp:      vector.now,
	})
	require.NoError(t, err)

	// Same proof, same circuit, a different element asked of it.
	tampered := make([]zk.Attribute, len(vector.attributes))
	copy(tampered, vector.attributes)
	tampered[0].Identifier = "age_over_21"

	require.Error(t, system.Verify(zk.VerificationRequest{
		Circuit:    circuit.Hash,
		DocType:    vector.docType,
		IssuerKeyX: vector.issuerKeyX,
		IssuerKeyY: vector.issuerKeyY,
		Transcript: vector.transcript,
		Attributes: tampered,
		Proof:      proof,
		Timestamp:  vector.now,
	}), "a proof of age_over_18 must not verify as a proof of age_over_21")
}

type googleVector struct {
	docType        string
	issuerKeyX     string
	issuerKeyY     string
	transcript     []byte
	deviceResponse []byte
	attributes     []zk.Attribute
	now            time.Time
}

// circuitForAttributes picks any circuit this build holds that proves over the
// given number of attributes.
func circuitForAttributes(system *longfellow.System, count int) (zk.Circuit, bool) {
	for _, circuit := range system.Circuits() {
		if circuit.NumAttributes == count {
			return circuit, true
		}
	}
	return zk.Circuit{}, false
}

func loadGoogleVector(t testing.TB) googleVector {
	t.Helper()

	file, err := os.Open(filepath.Join("testdata", "google_mdoc_vector_0.txt"))
	require.NoError(t, err)
	defer file.Close()

	fields := map[string]string{}
	scanner := bufio.NewScanner(file)
	// The DeviceResponse is a few kilobytes of hex on one line, past the
	// scanner's default limit.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		key, value, found := strings.Cut(strings.TrimSpace(scanner.Text()), " ")
		if !found {
			continue
		}
		fields[key] = value
	}
	require.NoError(t, scanner.Err())

	decode := func(key string) []byte {
		raw, ok := fields[key]
		require.True(t, ok, "fixture has no %s", key)
		decoded, err := hex.DecodeString(raw)
		require.NoError(t, err, "decode %s", key)
		return decoded
	}

	now, err := time.Parse(time.RFC3339, fields["now"])
	require.NoError(t, err)

	return googleVector{
		docType: fields["doctype"],
		// Passed as the 0x-prefixed strings upstream stores, which is the form
		// the C ABI takes: run_mdoc_prover reads the issuer key as text, not as
		// a point or a certificate.
		issuerKeyX:     fields["pkx"],
		issuerKeyY:     fields["pky"],
		transcript:     decode("transcript"),
		deviceResponse: decode("mdoc"),
		now:            now,
		attributes: []zk.Attribute{{
			Namespace:  string(decode("attr_namespace")),
			Identifier: string(decode("attr_id")),
			Value:      decode("attr_value"),
		}},
	}
}
