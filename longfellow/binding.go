// Adapted from google/longfellow-zk reference/verifier-service/server/zk/proofs.go
//
//	Copyright 2025 Google LLC
//	Licensed under the Apache License, Version 2.0 (the "License");
//	you may not use this file except in compliance with the License.
//	You may obtain a copy of the License at
//
//	    http://www.apache.org/licenses/LICENSE-2.0
//
//	Unless required by applicable law or agreed to in writing, software
//	distributed under the License is distributed on an "AS IS" BASIS,
//	WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//	See the License for the specific language governing permissions and
//	limitations under the License.
//
// Modifications from the original, each deliberate:
//
//   - run_mdoc_prover is bound as well. The reference service only verifies;
//     a wallet is the half that proves, and #724 names this as the new part.
//   - set_attribute refuses over-long input instead of silently clamping
//     cbor_value to 64 bytes. A clamped value produces a perfectly valid proof
//     about a value nobody requested, which for a credential is the wrong
//     failure mode.
//   - circuit_id is given a real ZkSpecStruct rather than a malloc'd one with
//     only num_attributes set. The other fields of that struct were never
//     initialised, and passing uninitialised memory across the ABI is worth not
//     copying even where it happens to be harmless.
//   - the package logs nothing. The original prints circuit loading and
//     verification progress with the standard logger, which a library embedded
//     in a wallet has no business doing.
package longfellow

/*
#cgo LDFLAGS: -lmdoc_static -lcrypto -lzstd

// The C++ runtime is not the same library everywhere, and getting it wrong is a
// link error at the very end of a long build.
//
// The NDK has no libstdc++; it ships LLVM's libc++, and a wallet wants the
// static one so nothing has to be shipped and loaded alongside. The negated
// constraint on the first line is not redundant: Go treats GOOS=android as
// satisfying the `linux` build constraint too, so `#cgo linux` alone would apply
// to both and put -lstdc++ back on the Android link line.
#cgo linux,!android LDFLAGS: -lstdc++
#cgo android LDFLAGS: -lc++_static -lc++abi
#cgo darwin LDFLAGS: -lc++

#include <stdlib.h>
#include <stddef.h>
#include <stdint.h>
#include <string.h>
#include "mdoc_zk.h"

// fill_attribute writes one RequestedAttribute, refusing anything that does not
// fit the fixed field widths rather than truncating it. Returns 0 on refusal.
//
// The reference implementation does `if (cborvaluelen > 64) cborvaluelen = 64;`
// and strncpy's the two strings, so an over-long value is quietly proved as its
// own first 64 bytes. Callers here have already validated through
// zk.Attribute.Validate; this is the second line of the same defence, at the
// point where the truncation would actually happen.
static int fill_attribute(RequestedAttribute* attrs, size_t index,
                          const char* namespace_id, size_t namespace_len,
                          const char* id, size_t id_len,
                          const uint8_t* cbor_value, size_t cbor_value_len) {
  if (namespace_len > 64 || id_len > 32 || cbor_value_len > 64) {
    return 0;
  }
  RequestedAttribute* a = &attrs[index];
  memset(a, 0, sizeof(*a));
  memcpy((char*)a->namespace_id, namespace_id, namespace_len);
  memcpy((char*)a->id, id, id_len);
  memcpy((char*)a->cbor_value, cbor_value, cbor_value_len);
  a->namespace_len = namespace_len;
  a->id_len = id_len;
  a->cbor_value_len = cbor_value_len;
  return 1;
}

static RequestedAttribute* alloc_attributes(size_t count) {
  return (RequestedAttribute*)calloc(count, sizeof(RequestedAttribute));
}

// any_spec returns a non-null ZkSpecStruct for circuit_id to be handed.
//
// circuit_id takes a spec but reads no field of it — mdoc_circuit_id.cc uses it
// only in the null check — so the identity it computes depends on the circuit
// bytes alone. That is what lets a circuit be identified before anything is
// known about it, which is the whole basis of loading by content rather than by
// filename. Verified by reading the function, not assumed from the signature.
static const ZkSpecStruct* any_spec(void) { return &kZkSpecs[0]; }
*/
import "C"

import (
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"github.com/privacybydesign/irmago/eudi/credentials/mdoc/zk"
)

// systemName is the only ZK system this library implements.
const systemName = "longfellow-libzk-v1"

// loadedCircuit is one circuit held in C memory, ready to prove or verify with,
// together with the library's own record of what it is.
//
// The bytes stay on the C heap for the life of the store rather than being
// copied in per call: they are the compressed form, a few hundred kilobytes, and
// the alternative is handing the same buffer across the boundary on every proof.
// What is emphatically not cached is the decompressed circuit — that is the
// ~88 MB the library rebuilds inside each call, and only one should ever be live.
type loadedCircuit struct {
	bytes *C.uint8_t
	size  C.size_t
	spec  *C.ZkSpecStruct
	info  zk.Circuit
}

// newLoadedCircuit identifies circuit bytes by content and prepares them for use.
//
// Identification is by content and nothing else. Google's loader requires the
// file to be NAMED its own hash and skips it otherwise; Multipaz parses the hash
// out of the filename and never checks it. Both make the filename load-bearing.
// Here the name is not consulted at all: the id is computed from the bytes, and
// the library's table decides whether it is a circuit we know.
func newLoadedCircuit(compressed []byte) (*loadedCircuit, error) {
	id, err := circuitID(compressed)
	if err != nil {
		return nil, err
	}
	return newLoadedCircuitWithID(compressed, id)
}

// newLoadedCircuitWithID prepares circuit bytes whose id is already known,
// skipping the expensive identification.
//
// Only a caller holding evidence that these bytes have this id may use it — in
// practice a Cache hit, where the id was computed by circuit_id over the same
// bytes on an earlier run. The id is still checked against the library's table,
// so a value that names no circuit at all is refused rather than carried; what
// this cannot catch is an id that names a DIFFERENT real circuit, which is why
// a cache has to be as trusted as the circuit files. See cache.go.
func newLoadedCircuitWithID(compressed []byte, id string) (*loadedCircuit, error) {
	spec, known := specFor(id)
	if !known {
		return nil, fmt.Errorf(
			"circuit %s is not one this build of longfellow knows: it is in no kZkSpecs entry", id)
	}

	return &loadedCircuit{
		bytes: (*C.uint8_t)(C.CBytes(compressed)),
		size:  C.size_t(len(compressed)),
		spec:  spec,
		info:  describe(spec, id),
	}, nil
}

// free releases the circuit's C memory. The spec is not freed: it points into
// the library's static kZkSpecs table.
func (c *loadedCircuit) free() {
	if c.bytes != nil {
		C.free(unsafe.Pointer(c.bytes))
		c.bytes = nil
		c.size = 0
	}
}

// circuitID computes longfellow's circuit_id over compressed circuit bytes:
// SHA-256 of the two parsed circuits' own ids.
//
// This is the function that makes a circuit self-identifying, and it cannot be
// reproduced in pure Go — the bytes have to be decompressed and parsed into two
// circuits first. A CGO_ENABLED=0 build therefore cannot check a circuit
// against its published hash, which is one of the reasons the prover lives in a
// module of its own rather than behind a build tag in irmago.
func circuitID(compressed []byte) (string, error) {
	if len(compressed) == 0 {
		return "", errors.New("no circuit bytes")
	}

	id := (*C.uint8_t)(C.malloc(32))
	defer C.free(unsafe.Pointer(id))

	bytes := (*C.uint8_t)(C.CBytes(compressed))
	defer C.free(unsafe.Pointer(bytes))

	if C.circuit_id(id, bytes, C.size_t(len(compressed)), C.any_spec()) == 0 {
		return "", errors.New("circuit_id failed: the bytes are not a parseable circuit pair")
	}
	return hex.EncodeToString(C.GoBytes(unsafe.Pointer(id), 32)), nil
}

// specFor looks up the library's own record of a circuit by its computed id.
//
// A circuit whose id is not in kZkSpecs is one this build of the library has
// never heard of, and is refused at load rather than at first use: the
// parameters in that table — version, attribute count, the two block-encoding
// sizes — are how the circuit is described to a reader, so a circuit we cannot
// describe is one we cannot honestly offer.
func specFor(id string) (*C.ZkSpecStruct, bool) {
	cSystem := C.CString(systemName)
	defer C.free(unsafe.Pointer(cSystem))
	cID := C.CString(id)
	defer C.free(unsafe.Pointer(cID))

	spec := C.find_zk_spec(cSystem, cID)
	if spec == nil {
		return nil, false
	}
	return spec, true
}

// describe turns the library's spec record into the description the boundary
// speaks in.
func describe(spec *C.ZkSpecStruct, id string) zk.Circuit {
	return zk.Circuit{
		System:        C.GoString(spec.system),
		Version:       int(spec.version),
		NumAttributes: int(spec.num_attributes),
		BlockEncHash:  int(spec.block_enc_hash),
		BlockEncSig:   int(spec.block_enc_sig),
		Hash:          id,
	}
}

// cAttributes marshals the attribute list into the C array the ABI reads.
//
// The returned free function must be called; the array is C memory. Attributes
// are positional on both sides, so the order given here is the order a verifier
// has to reproduce.
func cAttributes(attributes []zk.Attribute) (*C.RequestedAttribute, func(), error) {
	array := C.alloc_attributes(C.size_t(len(attributes)))
	if array == nil {
		return nil, func() {}, errors.New("could not allocate the attribute array")
	}
	free := func() { C.free(unsafe.Pointer(array)) }

	for i, attribute := range attributes {
		if err := attribute.Validate(); err != nil {
			free()
			return nil, func() {}, fmt.Errorf("attribute %d: %w", i, err)
		}

		namespace := C.CString(attribute.Namespace)
		identifier := C.CString(attribute.Identifier)
		value := C.CBytes(attribute.Value)

		ok := C.fill_attribute(array, C.size_t(i),
			namespace, C.size_t(len(attribute.Namespace)),
			identifier, C.size_t(len(attribute.Identifier)),
			(*C.uint8_t)(value), C.size_t(len(attribute.Value)))

		C.free(unsafe.Pointer(namespace))
		C.free(unsafe.Pointer(identifier))
		C.free(value)

		if ok == 0 {
			free()
			return nil, func() {}, fmt.Errorf(
				"attribute %d (%s/%s) does not fit the fixed widths of RequestedAttribute",
				i, attribute.Namespace, attribute.Identifier)
		}
	}
	return array, free, nil
}

// prove runs the native prover over an already-loaded circuit.
//
// This is the call the reference service does not have. Everything it needs is
// bytes the caller produced: a DeviceResponse carrying the document with its
// deviceSigned attached, the session transcript, the issuer key coordinates,
// and the attributes to open.
func prove(circuit *loadedCircuit, request zk.ProofRequest) ([]byte, error) {
	attributes, freeAttributes, err := cAttributes(request.Attributes)
	if err != nil {
		return nil, err
	}
	defer freeAttributes()

	document := (*C.uint8_t)(C.CBytes(request.DeviceResponse))
	defer C.free(unsafe.Pointer(document))
	transcript := (*C.uint8_t)(C.CBytes(request.Transcript))
	defer C.free(unsafe.Pointer(transcript))

	keyX := C.CString(request.IssuerKeyX)
	defer C.free(unsafe.Pointer(keyX))
	keyY := C.CString(request.IssuerKeyY)
	defer C.free(unsafe.Pointer(keyY))
	now := C.CString(zk.FormatTimestamp(request.Timestamp))
	defer C.free(unsafe.Pointer(now))

	var proof *C.uint8_t
	var proofLen C.size_t

	code := C.run_mdoc_prover(
		circuit.bytes, circuit.size,
		document, C.size_t(len(request.DeviceResponse)),
		keyX, keyY,
		transcript, C.size_t(len(request.Transcript)),
		attributes, C.size_t(len(request.Attributes)),
		now,
		&proof, &proofLen, circuit.spec)

	// The proof is malloc'd by the library and ours to free from here, including
	// on the paths that do not return it.
	if code != C.MDOC_PROVER_SUCCESS {
		if proof != nil {
			C.free(unsafe.Pointer(proof))
		}
		return nil, fmt.Errorf("run_mdoc_prover: %s", proverError(code))
	}
	defer C.free(unsafe.Pointer(proof))

	if proof == nil || proofLen == 0 {
		return nil, errors.New("run_mdoc_prover reported success but produced no proof")
	}

	// Copied onto the Go heap before the C allocation is released. KeepAlive
	// because nothing after this point refers to the circuit, and the store that
	// owns it must not be collected while the prover is still reading it.
	out := C.GoBytes(unsafe.Pointer(proof), C.int(proofLen))
	runtime.KeepAlive(circuit)
	return out, nil
}

// verify checks a proof against the cleartext claims it accompanies.
func verify(circuit *loadedCircuit, request zk.VerificationRequest) error {
	attributes, freeAttributes, err := cAttributes(request.Attributes)
	if err != nil {
		return err
	}
	defer freeAttributes()

	transcript := (*C.uint8_t)(C.CBytes(request.Transcript))
	defer C.free(unsafe.Pointer(transcript))
	proof := (*C.uint8_t)(C.CBytes(request.Proof))
	defer C.free(unsafe.Pointer(proof))

	keyX := C.CString(request.IssuerKeyX)
	defer C.free(unsafe.Pointer(keyX))
	keyY := C.CString(request.IssuerKeyY)
	defer C.free(unsafe.Pointer(keyY))
	now := C.CString(zk.FormatTimestamp(request.Timestamp))
	defer C.free(unsafe.Pointer(now))
	docType := C.CString(request.DocType)
	defer C.free(unsafe.Pointer(docType))

	code := C.run_mdoc_verifier(
		circuit.bytes, circuit.size,
		keyX, keyY,
		transcript, C.size_t(len(request.Transcript)),
		attributes, C.size_t(len(request.Attributes)),
		now,
		proof, C.size_t(len(request.Proof)),
		docType, circuit.spec)

	runtime.KeepAlive(circuit)

	if code != C.MDOC_VERIFIER_SUCCESS {
		return fmt.Errorf("run_mdoc_verifier: %s", verifierError(code))
	}
	return nil
}

// proverError names a prover return code.
//
// Named rather than numbered because the codes are the only diagnosis the
// library offers, and "code 18" in a bug report is a round trip through the
// header that nobody should have to make. DEVICE_SIGNED_MISSING in particular
// is the one a caller is most likely to hit and least likely to guess.
func proverError(code C.MdocProverErrorCode) string {
	switch code {
	case C.MDOC_PROVER_NULL_INPUT:
		return "null input"
	case C.MDOC_PROVER_INVALID_INPUT:
		return "invalid input"
	case C.MDOC_PROVER_CIRCUIT_PARSING_FAILURE:
		return "the circuit could not be parsed"
	case C.MDOC_PROVER_HASH_PARSING_FAILURE:
		return "the hash circuit could not be parsed"
	case C.MDOC_PROVER_GENERAL_FAILURE:
		return "general failure"
	default:
		return fmt.Sprintf("error code %d", int(code))
	}
}

func verifierError(code C.MdocVerifierErrorCode) string {
	switch code {
	case C.MDOC_VERIFIER_NULL_INPUT:
		return "null input"
	case C.MDOC_VERIFIER_INVALID_INPUT:
		return "invalid input"
	case C.MDOC_VERIFIER_CIRCUIT_PARSING_FAILURE:
		return "the circuit could not be parsed"
	case C.MDOC_VERIFIER_HASH_PARSING_FAILURE:
		return "the hash circuit could not be parsed"
	case C.MDOC_VERIFIER_GENERAL_FAILURE:
		return "general failure"
	case C.MDOC_VERIFIER_ARGUMENTS_TOO_SMALL:
		return "arguments too small"
	case C.MDOC_VERIFIER_ATTRIBUTE_NUMBER_MISMATCH:
		return "the circuit is built for a different number of attributes"
	case C.MDOC_VERIFIER_INVALID_ZK_SPEC_VERSION:
		return "invalid zk spec version"
	case C.MDOC_VERIFIER_INVALID_CBOR:
		return "an attribute value is not valid CBOR"
	default:
		return fmt.Sprintf("error code %d", int(code))
	}
}
