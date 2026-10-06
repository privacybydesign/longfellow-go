package longfellow_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/privacybydesign/irmago/eudi/credentials/mdoc"
	"github.com/privacybydesign/irmago/eudi/isomdoc"
	"github.com/privacybydesign/irmago/eudi/isomdoc/reader"
	"github.com/stretchr/testify/require"
	cose "github.com/veraison/go-cose"
)

// ============================================================
// THE WHOLE FLOW, WITH A REAL PROVER
// ============================================================
//
// irmago's own session tests fake the prover, because the real one lives in a
// module irmago does not link — that is the point of the split. A fake returns
// whatever it was told to, so it cannot disagree with the session about
// attribute ordering, circuit selection, or what a prover refuses. Everything in
// this file is the same wiring driven by the library itself, which can.
//
// What that buys, concretely: the session counts the elements actually disclosed
// rather than the elements requested, and picks a circuit built for exactly that
// many. Against a fake, an off-by-one there passes. Against the library it is a
// hard failure, because a circuit proves a fixed number of statements.

const (
	avDocType   = "eu.europa.ec.av.1"
	avNameSpace = "eu.europa.ec.av.1"
	testOrigin  = "https://verifier.example.com"
)

// readerSide is the verifier's half: it publishes an encryptionInfo, and opens
// what comes back.
type readerSide struct {
	key            *ecdsa.PrivateKey
	encryptionInfo string
}

func newReaderSide(t *testing.T) readerSide {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	nonce := make([]byte, 16)
	_, err = rand.Read(nonce)
	require.NoError(t, err)

	info, err := isomdoc.NewDCAPIEncryptionInfo(nonce, &key.PublicKey)
	require.NoError(t, err)
	encoded, err := cbor.Marshal(info)
	require.NoError(t, err)

	return readerSide{key: key, encryptionInfo: base64.RawURLEncoding.EncodeToString(encoded)}
}

func (r readerSide) transcript(t *testing.T) mdoc.SessionTranscript {
	t.Helper()
	transcript, err := mdoc.NewDCAPISessionTranscript(r.encryptionInfo, testOrigin)
	require.NoError(t, err)
	return transcript
}

func (r readerSide) open(t *testing.T, sealed isomdoc.DCAPIEncryptedResponse) mdoc.DeviceResponse {
	t.Helper()

	plaintext, err := isomdoc.OpenDCAPIResponse(sealed, r.key, r.transcript(t))
	require.NoError(t, err)

	var response mdoc.DeviceResponse
	require.NoError(t, cbor.Unmarshal(plaintext, &response))
	return response
}

// wallet is the Discloser: it answers with what the user agreed to.
type wallet struct {
	selections []isomdoc.Selection
	asked      isomdoc.DisclosureRequest
}

func (w *wallet) Disclose(request isomdoc.DisclosureRequest) ([]isomdoc.Selection, error) {
	w.asked = request
	return w.selections, nil
}

// issueAVCredential mints a credential and narrows it to the elements the user
// agreed to disclose, which is the state a Discloser hands back.
func issueAVCredential(t testing.TB, claims map[string]any, disclose ...string) (mdoc.MDoc, mdoc.DeviceSigner, *mdoc.TestIssuer) {
	t.Helper()

	issuer, err := mdoc.NewTestIssuer()
	require.NoError(t, err)
	holder, err := mdoc.GenerateDeviceSigner()
	require.NoError(t, err)

	credential, err := issuer.Issue(avDocType, avNameSpace, claims, holder.PublicKey())
	require.NoError(t, err)

	narrowed, err := mdoc.SelectiveDisclose(credential, avNameSpace, disclose)
	require.NoError(t, err)

	return *narrowed, holder, issuer
}

// zkDeviceRequest is a reader asking for elements and saying it will take a
// proof, offering the circuits it accepts.
func zkDeviceRequest(t *testing.T, offered []mdoc.ZkSystemSpec, elements ...string) []byte {
	t.Helper()

	wanted := mdoc.DataElements{}
	for _, element := range elements {
		wanted[element] = false
	}
	encodedZk, err := mdoc.ZkRequest{SystemSpecs: offered}.MarshalCBOR()
	require.NoError(t, err)

	items := mdoc.ItemsRequest{
		DocType:     avDocType,
		NameSpaces:  map[string]mdoc.DataElements{avNameSpace: wanted},
		RequestInfo: map[string]cbor.RawMessage{mdoc.ZkRequestKey: encodedZk},
	}

	docRequest, err := mdoc.NewDocRequest(items, nil)
	require.NoError(t, err)

	encoded, err := mdoc.DeviceRequest{
		Version:     mdoc.DeviceRequestVersion,
		DocRequests: []mdoc.DocRequest{docRequest},
	}.Encode()
	require.NoError(t, err)
	return encoded
}

// v6Specs are the circuits an AV reader offers.
//
// The captured EUDI AV reader offered v6 only, one revision behind the library,
// and we hold v7 as well -- so this is what makes the version intersection
// decide something rather than being a formality. Note the profile pins the
// SYSTEM (longfellow-libzk-v1), not the circuit revision; see the README.
func v6Specs(t *testing.T, prover *mdoc.ProverSystem) []mdoc.ZkSystemSpec {
	t.Helper()

	var offered []mdoc.ZkSystemSpec
	for _, spec := range prover.SystemSpecs() {
		if version, ok := spec.Version(); ok && version == 6 {
			offered = append(offered, spec)
		}
	}
	require.NotEmpty(t, offered, "the circuit directory must carry v6 circuits")
	return offered
}

// TestSessionProducesAProofTheReaderCanVerify is the whole loop: a reader asks
// and offers circuits, the session takes the ZK branch, the native library
// proves, the response is sealed and opened, and the reader verifies the proof
// through the relying party's own gate.
//
// Every layer between irmago and the C++ runs for real. The only thing faked is
// the user saying yes.
func TestSessionProducesAProofTheReaderCanVerify(t *testing.T) {
	system := openSystem(t)
	prover := mdoc.NewProverSystem(system)

	reader := newReaderSide(t)
	document, holder, issuer := issueAVCredential(t,
		map[string]any{"age_over_18": true, "age_over_21": false}, "age_over_18")

	user := &wallet{selections: []isomdoc.Selection{
		{DocType: avDocType, Document: document, Signer: holder},
	}}
	session := &isomdoc.Session{
		Discloser: user,
		ZkSystems: mdoc.NewZkSystemRepository(prover),
		Now:       func() time.Time { return time.Now() },
	}

	offered := v6Specs(t, prover)

	start := time.Now()
	sealed, err := session.Respond(isomdoc.Request{
		DeviceRequest:  zkDeviceRequest(t, offered, "age_over_18"),
		EncryptionInfo: reader.encryptionInfo,
		Origin:         testOrigin,
	})
	require.NoError(t, err)
	t.Logf("session responded in %v", time.Since(start).Round(time.Millisecond))

	// ---- what the reader received ----
	response := reader.open(t, sealed)
	require.Equal(t, mdoc.DeviceResponseVersionZk, response.Version,
		"a response carrying zkDocuments is a second-edition response")
	require.Len(t, response.ZkDocuments, 1)
	require.Empty(t, response.Documents,
		"the proof replaces the disclosure; sending both would disclose what the proof exists to hide")

	presented := response.ZkDocuments[0]
	require.Equal(t, avDocType, presented.DocumentData.DocType)
	require.NotEmpty(t, presented.Proof)

	// The wallet answered under one of the circuits the reader offered, and the
	// reader offered only v6 — so the newest circuit we hold is not the one used.
	require.Contains(t, specIDs(offered), presented.DocumentData.ZkSystemSpecID,
		"the proof must be under a circuit the reader actually offered")

	// ---- the relying party's own path ----
	//
	// §A.8 requires the accepted-circuit check before verification, because a
	// proof under a withdrawn circuit verifies perfectly well. VerifyZkDocument
	// is what enforces that ordering.
	accepted := mdoc.NewAcceptedCircuits(circuitHashes(t, offered)...)

	start = time.Now()
	spec, err := mdoc.VerifyZkDocument(
		presented, mdoc.NewZkSystemRepository(prover), accepted, reader.transcript(t), time.Now())
	require.NoError(t, err)
	t.Logf("reader verified in %v under %s", time.Since(start).Round(time.Millisecond), spec.ID)

	// The proof establishes the statements; it says nothing about whether the
	// issuer is trusted. That stays with the ordinary trust model, and the chain
	// the wallet sent is what it is evaluated against.
	require.NotEmpty(t, presented.DocumentData.MsoX5Chain)
	require.Equal(t, issuer.DSCert().Raw, presented.DocumentData.MsoX5Chain[0].Raw)
}

// TestSessionProvesWhatWasDisclosedNotWhatWasRequested is the case a fake prover
// cannot police.
//
// The reader asks for two elements; the user agrees to one. A circuit proves a
// fixed number of statements, so the session has to count what is actually being
// disclosed and pick the one-attribute circuit. Counting the request instead
// would select the two-attribute circuit, and the library would then refuse the
// mismatch — which is exactly the failure this asserts is absent.
func TestSessionProvesWhatWasDisclosedNotWhatWasRequested(t *testing.T) {
	system := openSystem(t)
	prover := mdoc.NewProverSystem(system)

	reader := newReaderSide(t)
	document, holder, _ := issueAVCredential(t,
		map[string]any{"age_over_18": true, "age_over_21": false}, "age_over_18")

	user := &wallet{selections: []isomdoc.Selection{
		{DocType: avDocType, Document: document, Signer: holder},
	}}
	session := &isomdoc.Session{
		Discloser: user,
		ZkSystems: mdoc.NewZkSystemRepository(prover),
	}

	offered := v6Specs(t, prover)

	sealed, err := session.Respond(isomdoc.Request{
		// Two elements asked for, one consented to.
		DeviceRequest:  zkDeviceRequest(t, offered, "age_over_18", "age_over_21"),
		EncryptionInfo: reader.encryptionInfo,
		Origin:         testOrigin,
	})
	require.NoError(t, err)

	response := reader.open(t, sealed)
	require.Len(t, response.ZkDocuments, 1)

	presented := response.ZkDocuments[0]
	require.Len(t, presented.DocumentData.IssuerSigned[avNameSpace], 1,
		"only the consented element is opened")

	// And the circuit used is the one built for a single attribute.
	spec, found := mdoc.NewZkSystemRepository(prover).SpecByID(presented.DocumentData.ZkSystemSpecID)
	require.True(t, found)
	count, ok := spec.NumAttributes()
	require.True(t, ok)
	require.Equal(t, int64(1), count)

	accepted := mdoc.NewAcceptedCircuits(circuitHashes(t, offered)...)
	_, err = mdoc.VerifyZkDocument(
		presented, mdoc.NewZkSystemRepository(prover), accepted, reader.transcript(t), time.Now())
	require.NoError(t, err)
}

// A circuit outside the scheme owner's published set must be refused BEFORE the
// proof is verified — the proof itself is sound, which is precisely why the hash
// check is the only thing standing between a withdrawn circuit and an accepted
// presentation.
func TestReaderRefusesAProofUnderAnUnacceptedCircuit(t *testing.T) {
	system := openSystem(t)
	prover := mdoc.NewProverSystem(system)

	reader := newReaderSide(t)
	document, holder, _ := issueAVCredential(t, map[string]any{"age_over_18": true}, "age_over_18")

	user := &wallet{selections: []isomdoc.Selection{
		{DocType: avDocType, Document: document, Signer: holder},
	}}
	session := &isomdoc.Session{
		Discloser: user,
		ZkSystems: mdoc.NewZkSystemRepository(prover),
	}

	offered := v6Specs(t, prover)
	sealed, err := session.Respond(isomdoc.Request{
		DeviceRequest:  zkDeviceRequest(t, offered, "age_over_18"),
		EncryptionInfo: reader.encryptionInfo,
		Origin:         testOrigin,
	})
	require.NoError(t, err)

	presented := reader.open(t, sealed).ZkDocuments[0]

	// A set that lists some other circuit entirely.
	accepted := mdoc.NewAcceptedCircuits(
		"0000000000000000000000000000000000000000000000000000000000000000")

	_, err = mdoc.VerifyZkDocument(
		presented, mdoc.NewZkSystemRepository(prover), accepted, reader.transcript(t), time.Now())
	require.ErrorContains(t, err, "not in the accepted circuit set")

	// And with no set configured at all, which fails closed for the same reason:
	// a verifier that has not been told what is accepted cannot attest that
	// anything is.
	_, err = mdoc.VerifyZkDocument(
		presented, mdoc.NewZkSystemRepository(prover), nil, reader.transcript(t), time.Now())
	require.Error(t, err)
}

// A build with no prover falls back to the plain presentation rather than
// failing, which is §A.8's own instruction. Exercised here with the real
// repository shape rather than a fake, because "no ZK system registered" is the
// ordinary state of every irmago build.
func TestSessionFallsBackWithNoProver(t *testing.T) {
	reader := newReaderSide(t)
	document, holder, _ := issueAVCredential(t, map[string]any{"age_over_18": true}, "age_over_18")

	user := &wallet{selections: []isomdoc.Selection{
		{DocType: avDocType, Document: document, Signer: holder},
	}}
	session := &isomdoc.Session{Discloser: user} // no ZkSystems at all

	sealed, err := session.Respond(isomdoc.Request{
		DeviceRequest:  zkDeviceRequest(t, nil, "age_over_18"),
		EncryptionInfo: reader.encryptionInfo,
		Origin:         testOrigin,
	})
	require.NoError(t, err, "absence of a prover is a fallback, not a failed session")

	response := reader.open(t, sealed)
	require.Empty(t, response.ZkDocuments)
	require.Len(t, response.Documents, 1, "the plain A.6 presentation is what travels")
}

func specIDs(specs []mdoc.ZkSystemSpec) []string {
	ids := make([]string, 0, len(specs))
	for _, spec := range specs {
		ids = append(ids, spec.ID)
	}
	return ids
}

func circuitHashes(t *testing.T, specs []mdoc.ZkSystemSpec) []string {
	t.Helper()

	hashes := make([]string, 0, len(specs))
	for _, spec := range specs {
		hash, ok := spec.CircuitHash()
		require.True(t, ok)
		hashes = append(hashes, hash)
	}
	return hashes
}

// ============================================================
// THE SAME FLOW THROUGH THE RELYING PARTY'S OWN ENTRY POINTS
// ============================================================
//
// The tests above drive the reader half by hand, because they predate
// eudi/isomdoc/reader. That package is what an actual relying party calls:
// Build makes the signed request, Open unseals the answer, and Verify applies
// the trust model, the accepted-circuit set and the proof in the order A.8
// requires. Driving THAT with the real prover is the whole deployment in one
// process — TestIssuer mints the credential, isomdoc.Session is the wallet,
// reader.Builder is the verifier — and the only fake left is the user saying
// yes.

// flowReader is a relying party whose trust model pins exactly one issuer: the
// one the test minted. Its own certificate is self-signed, which is fine here —
// the wallet session below runs without a reader trust model (reader
// authentication has its own fixtures and tests in irmago), so what this
// exercises is the issuer half of trust, which is the half a proof cannot
// supply.
func flowReader(t *testing.T, issuer *mdoc.TestIssuer, specs []mdoc.ZkSystemSpec, systems *mdoc.ZkSystemRepository) reader.Builder {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "longfellow-go flow test reader"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	certificate, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return reader.Builder{
		Chain:     []*x509.Certificate{certificate},
		Signer:    key,
		Algorithm: cose.AlgorithmES256,
		Specs:     specs,
		Verifier:  mdoc.NewVerifier([]*x509.Certificate{issuer.IACACert()}),
		ZkSystems: systems,
	}
}

// respondTo is the wallet's whole turn: take the reader's request as it would
// arrive over the DC API, and answer it.
func respondTo(t *testing.T, session *isomdoc.Session, request *reader.Request) isomdoc.DCAPIEncryptedResponse {
	t.Helper()

	sealed, err := session.Respond(isomdoc.Request{
		DeviceRequest:  request.DeviceRequest,
		EncryptionInfo: request.EncryptionInfo,
		Origin:         request.Origin,
	})
	require.NoError(t, err)
	return sealed
}

// TestFullFlowThroughTheRelyingPartyEntryPoints is issuance to verification
// with nothing hand-rolled: the reader builds its request with reader.Build,
// the wallet proves with the native library, and the reader opens and verifies
// with reader.Open and reader.Verify — issuer chain, accepted circuits,
// timestamp and proof, in that order.
func TestFullFlowThroughTheRelyingPartyEntryPoints(t *testing.T) {
	system := openSystem(t)
	prover := mdoc.NewProverSystem(system)
	systems := mdoc.NewZkSystemRepository(prover)

	document, holder, issuer := issueAVCredential(t,
		map[string]any{"age_over_18": true}, "age_over_18")

	relyingParty := flowReader(t, issuer, v6Specs(t, prover), systems)
	request, err := relyingParty.Build(testOrigin, avDocType, mdoc.DataElements{"age_over_18": false})
	require.NoError(t, err)

	session := &isomdoc.Session{
		Discloser: &wallet{selections: []isomdoc.Selection{
			{DocType: avDocType, Document: document, Signer: holder},
		}},
		ZkSystems: systems,
	}
	sealed := respondTo(t, session, request)

	plaintext, err := request.Open(sealed)
	require.NoError(t, err)

	verified, err := relyingParty.Verify(request, plaintext, avDocType, time.Now())
	require.NoError(t, err)
	require.Len(t, verified, 1)

	presentation := verified[0]
	require.True(t, presentation.ZeroKnowledge,
		"the reader offered circuits and the wallet holds a prover, so this must be the ZK branch")
	require.Equal(t, avDocType, presentation.DocType)
	require.NotNil(t, presentation.IssuerSigner)
	require.Equal(t, issuer.DSCert().Raw, presentation.IssuerSigner.Raw,
		"the issuer identity must be the one established through the pinned IACA, not merely claimed")

	items := presentation.Elements[avNameSpace]
	require.Len(t, items, 1, "one element was disclosed, so the proof covers one element")
	require.Equal(t, "age_over_18", items[0].ElementIdentifier)
	require.Equal(t, cbor.RawMessage{0xf5}, items[0].ElementValue, "0xf5 is CBOR true")
}

// TestFullFlowRefusesAForeignIssuer is the same loop against a relying party
// that pins a DIFFERENT issuer. The proof itself still verifies — it is a sound
// statement about a worthless key — so the refusal has to come from the trust
// model, and this pins that it does.
func TestFullFlowRefusesAForeignIssuer(t *testing.T) {
	system := openSystem(t)
	prover := mdoc.NewProverSystem(system)
	systems := mdoc.NewZkSystemRepository(prover)

	document, holder, _ := issueAVCredential(t,
		map[string]any{"age_over_18": true}, "age_over_18")

	stranger, err := mdoc.NewTestIssuer()
	require.NoError(t, err)

	relyingParty := flowReader(t, stranger, v6Specs(t, prover), systems)
	request, err := relyingParty.Build(testOrigin, avDocType, mdoc.DataElements{"age_over_18": false})
	require.NoError(t, err)

	session := &isomdoc.Session{
		Discloser: &wallet{selections: []isomdoc.Selection{
			{DocType: avDocType, Document: document, Signer: holder},
		}},
		ZkSystems: systems,
	}
	sealed := respondTo(t, session, request)

	plaintext, err := request.Open(sealed)
	require.NoError(t, err)

	_, err = relyingParty.Verify(request, plaintext, avDocType, time.Now())
	require.Error(t, err, "a proof under an unpinned issuer must be refused whole")
}

// TestFullFlowFallsBackPlainThroughTheSameEntryPoints is A.6's other half: the
// reader offers circuits, the wallet has no prover, and the same Build → Open →
// Verify loop must land on an ordinary signed disclosure the reader accepts.
func TestFullFlowFallsBackPlainThroughTheSameEntryPoints(t *testing.T) {
	system := openSystem(t)
	prover := mdoc.NewProverSystem(system)
	systems := mdoc.NewZkSystemRepository(prover)

	document, holder, issuer := issueAVCredential(t,
		map[string]any{"age_over_18": true}, "age_over_18")

	relyingParty := flowReader(t, issuer, v6Specs(t, prover), systems)
	request, err := relyingParty.Build(testOrigin, avDocType, mdoc.DataElements{"age_over_18": false})
	require.NoError(t, err)

	// The wallet: no ZkSystems registered, which is the ordinary state of a
	// build without the native prover.
	session := &isomdoc.Session{
		Discloser: &wallet{selections: []isomdoc.Selection{
			{DocType: avDocType, Document: document, Signer: holder},
		}},
	}
	sealed := respondTo(t, session, request)

	plaintext, err := request.Open(sealed)
	require.NoError(t, err)

	verified, err := relyingParty.Verify(request, plaintext, avDocType, time.Now())
	require.NoError(t, err)
	require.Len(t, verified, 1)
	require.False(t, verified[0].ZeroKnowledge, "no prover, so this must be the plain fallback")
	require.NotNil(t, verified[0].Plain)
	require.True(t, verified[0].Plain.Valid)
}
