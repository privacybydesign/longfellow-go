package longfellow_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/privacybydesign/irmago/eudi/credentials/mdoc"
	"github.com/privacybydesign/irmago/eudi/isomdoc"
	"github.com/stretchr/testify/require"
)

// ============================================================
// THE WHOLE CHAIN, ENDING AT SOMEONE ELSE'S VERIFIER
// ============================================================
//
// irmago's eudi/isomdoc/interop_live_test.go round-trips a PLAIN presentation
// against multipaz-verifier-server. It has to stop there: irmago never links the
// prover (#724 -- "irmago never compiles C++, not even in a tagged job"), so the
// one thing it cannot ask a third-party verifier to check is a proof. This module
// links both halves, which makes it the only place the whole chain fits in one
// process:
//
//	issuer    mdoc.NewTestIssuer mints an eu.europa.ec.av.1 credential
//	wallet    isomdoc.Session answers it, taking the ZK branch
//	prover    the native longfellow library builds the proof
//	verifier  multipaz-verifier-server asked for it, and checks it
//
// session_test.go already drives issuer, wallet and prover with nothing faked but
// the user saying yes -- and then verifies with OUR verifier. That is the gap this
// closes. A prover and a verifier built from the same source agree with each other
// by construction; the interesting question is whether a proof this wallet
// produced satisfies an implementation that shares no code with it. Theirs is
// Kotlin, it parses the response with its own CBOR, and it rebuilds the session
// transcript from its own state rather than trusting ours.
//
// SKIPPED unless the verifier answers, and unless LONGFELLOW_CIRCUITS names a
// circuit directory. Not a CI test. Stand the server up from irmago's checkout,
// which pins the commit:
//
//	docker compose --profile interop up --build -d multipaz-verifier
//
// This test does not run on a developer's host the way irmago's does: it needs
// the native prover, so it runs inside the image that carries it, and there
// 127.0.0.1 is the container rather than the verifier. MULTIPAZ_VERIFIER_URL is
// how the two are introduced -- see the run recipe at the bottom of this file.
//
// # What this does NOT establish
//
// Issuer trust fails, and is deliberately not asserted: the credential is signed
// by mdoc.NewTestIssuer, which this verifier has no reason to trust. That costs
// nothing here, because the verifier reports the proof and the issuer as separate
// result lines -- verifier.kt checks the proof first, then consults its trust
// list -- so an untrusted issuer does not stand between us and the answer.
//
// Reader authentication does not run at all, and that one is structural. See
// unauthenticatedWallet.

const (
	// verifierURLEnv overrides where the verifier is. The default suits a host
	// that somehow has the native library; the container run needs it, because
	// the verifier is a second container.
	verifierURLEnv     = "MULTIPAZ_VERIFIER_URL"
	defaultVerifierURL = "http://127.0.0.1:8006"

	// exchangeProtocol names the DC API exchange to set up, and it is NOT
	// isomdoc.DcApiProtocolIsoMdoc. Two vocabularies meet at this endpoint:
	// dcBegin takes a value of Multipaz's own Protocol enum (verifier.kt),
	// whereas org-iso-mdoc is the protocol IDENTIFIER that comes back in
	// dcRequestProtocol and goes out again as credentialProtocol. Sending the
	// identifier here is rejected with "Unknown protocol".
	exchangeProtocol = "w3c_dc_mdoc_api"

	// zkCannedRequestID selects the canned request that sets mdocUseZkp, which is
	// what makes the server attach a zkRequest listing the circuits it accepts.
	// Defined by Multipaz in AgeVerification.kt, and it asks for exactly one
	// element -- so the circuit the session picks is the one-attribute one.
	zkCannedRequestID = "age_over_18_zkp"
)

// verifierEndpoint resolves where the verifier is, once, into the two forms the
// exchange needs: the base URL to post to, and the host:port to dial.
//
// The host:port is also what gets declared to the server as its own `host`, which
// it stores and uses to build the SAN of its reader identity. That is inert while
// signRequest is false -- no identity is added -- but deriving it here rather
// than writing a literal keeps a repointed MULTIPAZ_VERIFIER_URL from quietly
// telling the verifier it lives somewhere it does not.
func verifierEndpoint(t *testing.T) (base, host string) {
	t.Helper()

	base = defaultVerifierURL
	if override := os.Getenv(verifierURLEnv); override != "" {
		base = strings.TrimSuffix(override, "/")
	}

	parsed, err := url.Parse(base)
	require.NoError(t, err, "%s is not a URL: %s", verifierURLEnv, base)
	require.NotEmpty(t, parsed.Host, "%s names no host: %s", verifierURLEnv, base)

	host = parsed.Host
	if parsed.Port() == "" {
		host = net.JoinHostPort(host, "8006")
	}
	return base, host
}

// skipUnlessMultipazVerifierRunning dials the verifier rather than a fixed
// address, so that pointing MULTIPAZ_VERIFIER_URL somewhere unreachable skips
// instead of failing deep inside the exchange.
func skipUnlessMultipazVerifierRunning(t *testing.T, base, host string) {
	t.Helper()

	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		t.Skipf("no multipaz-verifier-server at %s (%s); start it from irmago with: "+
			"docker compose --profile interop up --build -d multipaz-verifier", base, err)
	}
	_ = conn.Close()
}

// dcBeginResponse is the subset of the server's reply this needs.
type dcBeginResponse struct {
	SessionID         string `json:"sessionId"`
	DcRequestProtocol string `json:"dcRequestProtocol"`
	DcRequestString   string `json:"dcRequestString"`
	Error             string `json:"error"`
}

// resultLine is one key/value the verifier reports about what it received.
type resultLine struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type resultData struct {
	Pages []struct {
		Lines []resultLine `json:"lines"`
	} `json:"pages"`
}

func (r resultData) lines() []resultLine {
	var all []resultLine
	for _, page := range r.Pages {
		all = append(all, page.Lines...)
	}
	return all
}

func (r resultData) find(key string) (string, bool) {
	for _, line := range r.lines() {
		if line.Key == key {
			return line.Value, true
		}
	}
	return "", false
}

func postVerifierJSON(t *testing.T, base, path string, body any, into any) {
	t.Helper()

	encoded, err := json.Marshal(body)
	require.NoError(t, err)

	response, err := http.Post(base+path, "application/json", bytes.NewReader(encoded))
	require.NoError(t, err)
	defer response.Body.Close()

	var raw json.RawMessage
	require.NoError(t, json.NewDecoder(response.Body).Decode(&raw))
	require.Equal(t, http.StatusOK, response.StatusCode, "POST %s: %s", path, raw)
	require.NoError(t, json.Unmarshal(raw, into))
}

// unauthenticatedWallet answers the elements the reader asked for, even though
// the session marked every one of them withheld.
//
// # Why it has to
//
// Over the DC API an unauthenticated reader is entitled to NOTHING from this
// wallet: 18013-7 Clause 7 lifts the 7.2.1 prohibition that would otherwise force
// mandatory elements out, and Yivi's own policy fills the gap, so
// mdoc.ReleasableWithoutReaderAuth returns an empty releasable set and Permitted
// arrives empty. Disclosing from Permitted would mean proving nothing at all.
//
// And this reader cannot authenticate to us, whatever it is asked to do. Setting
// signRequest does make the server sign -- but with readerAuthAll, the
// request-wide COSE_Sign1 from the 2025 edition of 18013-5, sitting beside
// docRequests rather than inside one. irmago implements the 2021 edition, where
// there is exactly one readerAuth per DocRequest and no request-wide signature at
// all (mdoc.DeviceRequest has no field for it, and
// TestReaderAuthPerDocRequestBinding already records the divergence). So irmago
// finds no readerAuth, correctly reports that the reader did not authenticate,
// and withholds everything -- whether or not a signature was asked for.
//
// Disclosing anyway is the only way to reach the subject of this test. The test
// pins the situation below rather than papering over it: the day irmago learns
// readerAuthAll, those assertions fail and this type should go. irmago's own live
// interop test makes the same trade with a fixed selection; the difference is
// that this one says so.
type unauthenticatedWallet struct {
	t          *testing.T
	credential *mdoc.MDoc
	signer     mdoc.DeviceSigner

	// asked is what the session resolved the reader's request into. Recorded
	// because it is visible only here: Respond hands back a response sealed to the
	// reader, so the caller cannot otherwise see what was asked.
	asked     isomdoc.DisclosureRequest
	disclosed []string
}

func (w *unauthenticatedWallet) Disclose(request isomdoc.DisclosureRequest) ([]isomdoc.Selection, error) {
	w.asked = request

	var selections []isomdoc.Selection
	for _, document := range request.Documents {
		w.t.Logf("docType=%s authenticated=%v zk=%v requested=%v permitted=%v",
			document.DocType, document.Authenticated(), document.ZkRequested(),
			document.Requested.NameSpaces, document.Permitted.NameSpaces)

		// Requested, not Permitted -- see the type comment. Sorted because a
		// circuit proves a fixed list of statements and Go randomises map order.
		names := make([]string, 0, len(document.Requested.NameSpaces[avNameSpace]))
		for name := range document.Requested.NameSpaces[avNameSpace] {
			names = append(names, name)
		}
		sort.Strings(names)

		narrowed, err := mdoc.SelectiveDisclose(w.credential, avNameSpace, names)
		if err != nil {
			return nil, err
		}
		w.disclosed = append(w.disclosed, names...)
		selections = append(selections, isomdoc.Selection{
			DocType:  document.DocType,
			Document: *narrowed,
			Signer:   w.signer,
		})
	}
	return selections, nil
}

// TestLiveZkRoundTripAgainstMultipazVerifier is the four-corner run.
func TestLiveZkRoundTripAgainstMultipazVerifier(t *testing.T) {
	base, host := verifierEndpoint(t)
	skipUnlessMultipazVerifierRunning(t, base, host)

	system := openSystem(t)
	prover := mdoc.NewProverSystem(system)

	// ---- their request ----------------------------------------------------
	//
	// signRequest is false deliberately. Asking for a signature changes nothing
	// this wallet can act on -- it produces a readerAuthAll irmago does not
	// implement -- so requesting one would only imply reader auth was tested.
	var begin dcBeginResponse
	postVerifierJSON(t, base, "/verifier/dcBegin", map[string]any{
		"format":                 "mdoc",
		"docType":                avDocType,
		"requestId":              zkCannedRequestID,
		"rawDcql":                "",
		"multiDocumentRequestId": "",
		"protocol":               exchangeProtocol,
		"origin":                 testOrigin,
		"host":                   host,
		"signRequest":            false,
		"encryptResponse":        true,
	}, &begin)

	require.Empty(t, begin.Error, "the verifier refused to mint the request")
	require.Equal(t, isomdoc.DcApiProtocolIsoMdoc, begin.DcRequestProtocol)
	require.NotEmpty(t, begin.SessionID)

	request, err := isomdoc.RequestFromDcApi([]byte(begin.DcRequestString), testOrigin)
	require.NoError(t, err)

	// ---- our answer -------------------------------------------------------
	issuer, err := mdoc.NewTestIssuer()
	require.NoError(t, err)
	holder, err := mdoc.GenerateDeviceSigner()
	require.NoError(t, err)
	credential, err := issuer.Issue(avDocType, avNameSpace,
		map[string]any{"age_over_18": true, "age_over_21": false}, holder.PublicKey())
	require.NoError(t, err)

	user := &unauthenticatedWallet{t: t, credential: credential, signer: holder}
	session := &isomdoc.Session{
		Discloser: user,
		ZkSystems: mdoc.NewZkSystemRepository(prover),
	}

	start := time.Now()
	sealed, err := session.Respond(request)
	require.NoError(t, err)
	t.Logf("proved and sealed in %v, disclosing %v",
		time.Since(start).Round(time.Millisecond), user.disclosed)

	// What the reader asked for, read back from the parse rather than assumed. A
	// canned request that stopped setting mdocUseZkp would otherwise leave this
	// test quietly round-tripping a plain disclosure -- which irmago's live test
	// already covers, and which this one exists to go beyond.
	require.Len(t, user.asked.Documents, 1)
	asked := user.asked.Documents[0]
	require.True(t, asked.ZkRequested(),
		"the %s canned request must carry a zkRequest; without one this is the plain round trip", zkCannedRequestID)
	require.NotEmpty(t, asked.Zk.SystemSpecs,
		"the reader offered no circuits, so there is nothing to agree on")
	require.True(t, session.ZeroKnowledge,
		"the session answered in the clear although the reader offered circuits: no proof reached the verifier")

	// The readerAuthAll divergence, pinned so that closing it is noticed here.
	// When irmago learns to verify a request-wide signature these two fail, and
	// unauthenticatedWallet should be replaced by one that discloses what it is
	// actually entitled to.
	require.False(t, asked.Authenticated(),
		"the reader authenticated: irmago now verifies readerAuthAll, so this test should disclose from Permitted")
	require.Empty(t, asked.Permitted.NameSpaces,
		"something is now releasable to an unauthenticated reader; revisit what this test discloses")

	// ---- hand it back -----------------------------------------------------
	encoded, err := cbor.Marshal(sealed)
	require.NoError(t, err)
	credentialResponse, err := json.Marshal(map[string]any{
		"response": base64.RawURLEncoding.EncodeToString(encoded),
	})
	require.NoError(t, err)

	var result resultData
	postVerifierJSON(t, base, "/verifier/dcGetData", map[string]any{
		"sessionId":          begin.SessionID,
		"credentialProtocol": isomdoc.DcApiProtocolIsoMdoc,
		"credentialResponse": string(credentialResponse),
	}, &result)

	for _, line := range result.lines() {
		t.Logf("  %-16s %s", line.Key, line.Value)
	}

	// ---- what they made of it ---------------------------------------------
	//
	// Keyed on the labels verifier.kt emits rather than on its prose, with one
	// exception: "was not found" is the spec-ID lookup failing, and that is worth
	// naming because it is the likeliest way this breaks. The verifier resolves
	// zkSystemSpecId by exact string match against its own catalogue, and the two
	// IDs are built independently -- ours in mdoc.specForCircuit, theirs from the
	// circuit's filename. Nothing but this test compares them.
	proof, ok := result.find("ZK proof")
	require.True(t, ok,
		"the verifier reported no ZK proof line: it did not take the zkDocuments path at all")
	require.NotContains(t, proof, "was not found",
		"the verifier could not resolve our zkSystemSpecId against its own circuits: %s", proof)

	// "ZK Verification" is emitted only from verifier.kt's catch block, so its
	// presence means verification threw.
	if failure, thrown := result.find("ZK Verification"); thrown {
		require.Fail(t, "the verifier rejected the proof", failure)
	}

	// The proven element surfaced on their side, with the value we issued.
	value, ok := result.find("age_over_18")
	require.True(t, ok, "the verifier did not report the element the proof is about")
	require.Contains(t, strings.ToLower(value), "true")

	// Issuer trust is reported, not asserted -- see the header.
	if issuerLine, reported := result.find("Issuer"); reported {
		t.Logf("issuer trust (not asserted): %s", issuerLine)
	}
}

// ============================================================
// RUNNING IT
// ============================================================
//
// Two containers that have to find each other, plus a circuit directory this
// module deliberately does not vendor (#724 forbids prebuilt binaries in the
// module graph, and a circuit is a build artefact of the same library).
//
// 1. the verifier, from an irmago checkout:
//
//	docker compose --profile interop up --build -d multipaz-verifier
//
// 2. this test, in the image that carries the prover:
//
//	docker run --rm \
//	  -v "$PWD:/work" -w /work \
//	  -v "/path/to/circuits:/circuits:ro" \
//	  -e LONGFELLOW_CIRCUITS=/circuits \
//	  -e MULTIPAZ_VERIFIER_URL=http://host.docker.internal:8006 \
//	  --add-host host.docker.internal:host-gateway \
//	  longfellow-build:latest \
//	  go test ./longfellow/ -run TestLiveZkRoundTrip -v -count=1
//
// --add-host is what makes that work on Linux as well as Docker Desktop; the
// name resolves on its own only on the latter. Joining the compose network and
// addressing multipaz-verifier.localhost:8006 works too, and is the better
// option if this is ever wired into CI.
