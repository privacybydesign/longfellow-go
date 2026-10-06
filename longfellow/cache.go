package longfellow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ============================================================
// VERIFIED-HASH CACHE
// ============================================================
//
// Identifying a circuit costs about 1.2 seconds: circuit_id has to decompress
// ~90 MB and parse two circuits before a hash exists. A wallet holding the four
// v6 circuits the captured EUDI AV reader offered pays that four times on every
// launch, for an answer that cannot have changed unless the files did.
//
// A cache turns that into a SHA-256 of a few hundred kilobytes — microseconds,
// pure Go, no decompression. The expensive call establishes once that these
// bytes are the circuit with that published id; the cheap digest afterwards only
// has to establish that these are still those bytes.
//
// # What a cache is trusted with, which is not nothing
//
// A cache maps file digest to circuit id, so anything that can write the cache
// can make a circuit load under an id its bytes do not have — and that id is
// what the relying party's accepted-circuit check compares. A cache must
// therefore be protected exactly as well as the circuit files themselves. That
// is the ordinary case, since both live in app-private storage, but it is a real
// assumption rather than a free lunch, and it is why no cache is used unless a
// caller passes one.
//
// MapCache is the variant with no such assumption: the mapping is compiled into
// the binary and ships signed with the application. For bundled circuits that is
// strictly better than a file, and it is the recommended form.

// Cache remembers which circuit id a given file's bytes were verified to have.
//
// Lookup misses must be cheap and must never be fatal: a miss simply means the
// circuit is identified the slow, authoritative way. Implementations may be
// consulted concurrently.
type Cache interface {
	// Lookup returns the circuit id previously recorded for a file digest.
	Lookup(fileDigest string) (circuitID string, ok bool)

	// Record stores a verified mapping. An error is reported to the caller of
	// Open but does not fail the load: a cache that cannot be written is a
	// performance problem, not a correctness one.
	Record(fileDigest, circuitID string) error
}

// FileDigest is the key a cache is addressed by: SHA-256 of the circuit file's
// bytes, in lowercase hex.
//
// Emphatically NOT the circuit id. The two are different values over the same
// file — the id is SHA-256 of the two parsed circuits' own ids, which is why it
// cannot be computed without decompressing. This one is a plain digest of the
// file, and its only job is to detect that the bytes changed.
func FileDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// MapCache is an immutable cache over a mapping known ahead of time.
//
// This is the form to prefer for circuits bundled with an application: generate
// the mapping at build time, compile it in, and it ships signed with everything
// else. Recording is a no-op, so an unknown circuit is identified the slow way
// and simply not remembered.
type MapCache map[string]string

// Lookup implements Cache.
func (m MapCache) Lookup(fileDigest string) (string, bool) {
	id, ok := m[fileDigest]
	return id, ok
}

// Record implements Cache and deliberately does nothing: a compiled-in mapping
// is not something a running process gets to add to.
func (m MapCache) Record(string, string) error { return nil }

// FileCache is a Cache persisted as JSON next to wherever the caller wants it.
//
// Suited to circuits that arrive after install — downloaded, or provisioned —
// where a compiled-in mapping cannot exist. Reads are served from memory; a
// write rewrites the whole file, which is affordable because the file holds one
// short line per circuit and is only written when something new is identified.
type FileCache struct {
	path    string
	mutex   sync.RWMutex
	entries map[string]string
}

// cacheFile is the on-disk shape. Versioned so a later format change can be
// recognised rather than misread: an unknown version is treated as an empty
// cache, which costs one slow startup and no correctness.
type cacheFile struct {
	Version  int               `json:"version"`
	Circuits map[string]string `json:"circuits"`
}

const cacheFormatVersion = 1

// OpenFileCache reads a cache from path, treating a missing, unreadable or
// unrecognised file as an empty one.
//
// It does not report those as errors on purpose. Every one of them means the
// same thing operationally — nothing is remembered yet — and a wallet that
// refused to start because a performance cache was corrupt would be trading a
// real failure for an imaginary one.
func OpenFileCache(path string) *FileCache {
	cache := &FileCache{path: path, entries: map[string]string{}}

	content, err := os.ReadFile(path)
	if err != nil {
		return cache
	}

	var stored cacheFile
	if err := json.Unmarshal(content, &stored); err != nil || stored.Version != cacheFormatVersion {
		return cache
	}
	for digest, id := range stored.Circuits {
		cache.entries[digest] = id
	}
	return cache
}

// Lookup implements Cache.
func (c *FileCache) Lookup(fileDigest string) (string, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	id, ok := c.entries[fileDigest]
	return id, ok
}

// Record implements Cache, rewriting the file so the mapping survives a restart.
//
// The write is atomic — a temporary file in the same directory, then a rename —
// because a half-written cache read back on the next launch is exactly the
// corruption OpenFileCache would then have to shrug off.
func (c *FileCache) Record(fileDigest, circuitID string) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.entries[fileDigest] == circuitID {
		return nil
	}
	c.entries[fileDigest] = circuitID

	encoded, err := json.MarshalIndent(cacheFile{
		Version:  cacheFormatVersion,
		Circuits: c.entries,
	}, "", "  ")
	if err != nil {
		return err
	}

	directory := filepath.Dir(c.path)
	temporary, err := os.CreateTemp(directory, ".circuits-*.json")
	if err != nil {
		return fmt.Errorf("write circuit cache: %w", err)
	}
	name := temporary.Name()

	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		os.Remove(name)
		return fmt.Errorf("write circuit cache: %w", err)
	}
	if err := temporary.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("write circuit cache: %w", err)
	}
	if err := os.Rename(name, c.path); err != nil {
		os.Remove(name)
		return fmt.Errorf("write circuit cache: %w", err)
	}
	return nil
}

// Entries returns a copy of what the cache holds, which is what a build step
// would turn into a MapCache literal.
func (c *FileCache) Entries() map[string]string {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	copied := make(map[string]string, len(c.entries))
	for digest, id := range c.entries {
		copied[digest] = id
	}
	return copied
}
