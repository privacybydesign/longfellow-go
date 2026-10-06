// Package longfellow implements irmago's zk.System over Google's longfellow-zk
// library, linked from source.
//
// # What this module is for
//
// irmago declares the boundary — eudi/credentials/mdoc/zk, a leaf package that
// imports only the standard library — and cannot implement it, because doing so
// means compiling C++. This module is the implementation. It imports irmago's
// leaf package; irmago does not import this. An application that wants a prover
// imports both and calls Open, then registers the result. A build that does not
// simply has no ZK system, which AV Annex A §A.8 makes the ordinary fallback
// rather than an error.
//
// # Building
//
// This package links longfellow-zk statically and cannot be built without it.
// The library is built from source — no prebuilt binary enters this module's
// graph, which is a constraint of irmago #724 rather than a preference. Point
// cgo at the install tree:
//
//	export LONGFELLOW_INSTALL=/src/longfellow-zk/install
//	export CGO_CFLAGS="-I$LONGFELLOW_INSTALL/include"
//	export CGO_LDFLAGS="-L$LONGFELLOW_INSTALL/lib"
//	go build ./...
//
// scripts/build-module.sh does this inside the longfellow-build image, which is
// where the library already is. The link paths are deliberately not baked into
// the cgo directives: the reference service hardcodes ../../install, which works
// only from its own directory in its own checkout.
//
// # Circuits are assets, not something this can generate
//
// generate_circuit only emits the newest version the library supports, while the
// circuits AV readers actually offer are v6 and the library is at v7 -- so asking
// it for a v6 circuit returns CIRCUIT_GENERATION_INVALID_ZK_SPEC_VERSION. Every deployment must
// therefore ship circuits it obtained elsewhere, and Open verifies each one by
// recomputing its id rather than trusting where it came from.
package longfellow

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/privacybydesign/irmago/eudi/credentials/mdoc/zk"
)

// System is a longfellow-libzk-v1 prover and verifier over a fixed set of
// circuits.
//
// Safe for concurrent use. The mutex is not about the native code, which holds
// no mutable global state, but about Close: a proof in flight is reading C
// memory that Close would release under it.
type System struct {
	mutex    sync.RWMutex
	circuits map[string]*loadedCircuit
	closed   bool
}

var _ zk.System = (*System)(nil)

// Open loads every circuit in fsys and returns a System over them.
//
// Each file is identified by its content: the id is recomputed with
// circuit_id and looked up in the library's own table. A file that is not a
// parseable circuit, or is one this build does not know, fails the call rather
// than being skipped — a wallet that silently came up with fewer circuits than
// its operator installed would fall back to plain presentation for reasons
// nobody could see. The one exception is a file the caller can plainly see is
// not a circuit: README and dotfiles are ignored by name.
//
// Loading is the expensive part of startup: circuit_id decompresses and parses
// each circuit, which costs roughly the same memory as proving does. It happens
// once, and only the compressed bytes are retained.
func Open(fsys fs.FS, options ...Option) (*System, error) {
	settings := config{}
	for _, option := range options {
		option(&settings)
	}

	circuits := map[string]*loadedCircuit{}

	cleanup := func() {
		for _, circuit := range circuits {
			circuit.free()
		}
	}

	err := fs.WalkDir(fsys, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || skipByName(entry.Name()) {
			return nil
		}

		content, err := fs.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("read circuit %s: %w", path, err)
		}

		circuit, err := identify(content, settings.cache)
		if err != nil {
			return fmt.Errorf("circuit %s: %w", path, err)
		}

		// Two files holding the same circuit is not an error worth failing on,
		// but keeping both would mean paying for one of them twice.
		if existing, duplicate := circuits[circuit.info.Hash]; duplicate {
			existing.free()
		}
		circuits[circuit.info.Hash] = circuit
		return nil
	})
	if err != nil {
		cleanup()
		return nil, err
	}
	if len(circuits) == 0 {
		return nil, fmt.Errorf("no circuits found")
	}
	return &System{circuits: circuits}, nil
}

// OpenDir is Open over a directory on disk.
func OpenDir(dir string, options ...Option) (*System, error) {
	system, err := Open(os.DirFS(dir), options...)
	if err != nil {
		return nil, fmt.Errorf("load circuits from %s: %w", filepath.Clean(dir), err)
	}
	return system, nil
}

// Option configures Open.
type Option func(*config)

type config struct {
	cache Cache
}

// WithCache makes Open consult a verified-hash cache before identifying a
// circuit the slow way, and record what it learns.
//
// Worth roughly 1.2 seconds per circuit per launch. Read cache.go before
// choosing one: a cache is trusted to the same degree as the circuit files, and
// MapCache — a mapping compiled into the binary — is the variant that carries no
// such assumption.
func WithCache(cache Cache) Option {
	return func(c *config) { c.cache = cache }
}

// identify turns circuit bytes into a loaded circuit, using the cache when it
// can and falling back to full identification when it cannot.
//
// The fallback is the important part. A cache entry that no longer names a
// circuit this build knows — a downgraded library, a format change, a corrupted
// file — must cost a slow startup and nothing else. Nothing a cache says can
// make a circuit fail to load that would otherwise have loaded.
func identify(content []byte, cache Cache) (*loadedCircuit, error) {
	if cache == nil {
		return newLoadedCircuit(content)
	}

	digest := FileDigest(content)
	if id, hit := cache.Lookup(digest); hit {
		if circuit, err := newLoadedCircuitWithID(content, id); err == nil {
			return circuit, nil
		}
		// The entry was stale or wrong; identify properly and correct it below.
	}

	circuit, err := newLoadedCircuit(content)
	if err != nil {
		return nil, err
	}

	// A cache that cannot be written is a performance problem, not a
	// correctness one, so the error is deliberately dropped here rather than
	// failing a load that has already succeeded.
	_ = cache.Record(digest, circuit.info.Hash)
	return circuit, nil
}

func skipByName(name string) bool {
	return name == "README.md" || name == "" || name[0] == '.'
}

// Close releases the circuits' native memory. The System is unusable afterwards
// and reports every call as an error rather than dereferencing freed memory.
func (s *System) Close() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	for _, circuit := range s.circuits {
		circuit.free()
	}
	s.circuits = nil
	s.closed = true
	return nil
}

// Name is the ZK system identifier.
func (s *System) Name() string { return systemName }

// Circuits lists what this system holds, newest version first and then by
// attribute count, so the order a reader sees is stable across runs. Every hash
// here was recomputed from the circuit's own bytes at Open.
func (s *System) Circuits() []zk.Circuit {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	list := make([]zk.Circuit, 0, len(s.circuits))
	for _, circuit := range s.circuits {
		list = append(list, circuit.info)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Version != list[j].Version {
			return list[i].Version > list[j].Version
		}
		if list[i].NumAttributes != list[j].NumAttributes {
			return list[i].NumAttributes < list[j].NumAttributes
		}
		return list[i].Hash < list[j].Hash
	})
	return list
}

// Prove produces a proof under the circuit the request names.
//
// Expensive: seconds of CPU and, on the current library, on the order of 150 MB
// resident while it runs.
func (s *System) Prove(request zk.ProofRequest) ([]byte, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}

	circuit, err := s.circuit(request.Circuit)
	if err != nil {
		return nil, err
	}

	s.mutex.RLock()
	defer s.mutex.RUnlock()
	if s.closed {
		return nil, fmt.Errorf("system is closed")
	}
	if got, want := len(request.Attributes), circuit.info.NumAttributes; got != want {
		return nil, fmt.Errorf(
			"circuit %s opens %d attributes and %d were requested", circuit.info.Hash, want, got)
	}
	return prove(circuit, request)
}

// Verify checks a proof. A nil return means the statements hold; it says
// nothing about whether the issuer whose key they are stated against is one
// anybody should trust.
func (s *System) Verify(request zk.VerificationRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}

	circuit, err := s.circuit(request.Circuit)
	if err != nil {
		return err
	}

	s.mutex.RLock()
	defer s.mutex.RUnlock()
	if s.closed {
		return fmt.Errorf("system is closed")
	}
	if got, want := len(request.Attributes), circuit.info.NumAttributes; got != want {
		return fmt.Errorf(
			"circuit %s opens %d attributes and %d were presented", circuit.info.Hash, want, got)
	}
	return verify(circuit, request)
}

// circuit resolves a requested circuit hash, reporting zk.ErrNoCircuit when it
// is not held — the one failure callers are meant to treat as a fallback rather
// than a fault.
func (s *System) circuit(hash string) (*loadedCircuit, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	circuit, held := s.circuits[hash]
	if !held {
		return nil, fmt.Errorf("%w: %s", zk.ErrNoCircuit, hash)
	}
	return circuit, nil
}
