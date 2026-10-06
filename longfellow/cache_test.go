package longfellow_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/privacybydesign/longfellow-go/longfellow"
	"github.com/stretchr/testify/require"
)

// A cold open identifies every circuit the slow way; a warm one serves the same
// answers from the cache. The saving is the whole point, so it is asserted
// rather than described — with a generous margin, because what matters is the
// order of magnitude and not the exact figure on any one machine.
func TestFileCacheMakesASecondOpenFast(t *testing.T) {
	dir := circuitDir(t)

	cachePath := filepath.Join(t.TempDir(), "circuits.json")
	cache := longfellow.OpenFileCache(cachePath)

	cold := time.Now()
	first, err := longfellow.OpenDir(dir, longfellow.WithCache(cache))
	require.NoError(t, err)
	coldTook := time.Since(cold)
	circuits := first.Circuits()
	require.NoError(t, first.Close())

	require.NotEmpty(t, circuits)
	t.Logf("cold: %d circuits in %v", len(circuits), coldTook.Round(time.Millisecond))

	// Everything learned survives a restart, so a fresh cache object over the
	// same file is what a second launch would see.
	warmCache := longfellow.OpenFileCache(cachePath)
	require.Len(t, warmCache.Entries(), len(circuits),
		"every circuit identified should have been recorded")

	warm := time.Now()
	second, err := longfellow.OpenDir(dir, longfellow.WithCache(warmCache))
	require.NoError(t, err)
	warmTook := time.Since(warm)
	defer second.Close()

	t.Logf("warm: %d circuits in %v (%.0fx faster)",
		len(second.Circuits()), warmTook.Round(time.Millisecond),
		float64(coldTook)/float64(warmTook))

	require.Equal(t, circuits, second.Circuits(),
		"a cached open must produce exactly the same circuits as a cold one")
	require.Less(t, warmTook*4, coldTook, "the cache should save most of the load time")
}

// The cache is keyed on the file's bytes, so changed bytes miss and are
// identified afresh. This is what stops a cache from vouching for a file it
// never saw.
func TestFileCacheMissesWhenTheBytesChange(t *testing.T) {
	source := circuitDir(t)
	entries, err := os.ReadDir(source)
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(source, entries[0].Name()))
	require.NoError(t, err)

	staging := t.TempDir()
	circuitPath := filepath.Join(staging, "circuit")
	require.NoError(t, os.WriteFile(circuitPath, content, 0o644))

	cachePath := filepath.Join(t.TempDir(), "circuits.json")
	cache := longfellow.OpenFileCache(cachePath)

	system, err := longfellow.OpenDir(staging, longfellow.WithCache(cache))
	require.NoError(t, err)
	require.NoError(t, system.Close())
	require.Len(t, cache.Entries(), 1)

	// Corrupt the file. The digest no longer matches, so the recorded entry
	// cannot be applied to it and the load fails on its own merits.
	damaged := append([]byte{}, content...)
	damaged[len(damaged)/2] ^= 0xff
	require.NoError(t, os.WriteFile(circuitPath, damaged, 0o644))

	_, err = longfellow.OpenDir(staging, longfellow.WithCache(longfellow.OpenFileCache(cachePath)))
	require.Error(t, err, "damaged bytes must not be vouched for by a cache entry for the original")
}

// Nothing a cache says may make a circuit fail to load that would otherwise
// have loaded. A stale entry costs a slow startup and is then corrected.
func TestAStaleCacheEntryFallsBackAndIsCorrected(t *testing.T) {
	source := circuitDir(t)
	entries, err := os.ReadDir(source)
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(source, entries[0].Name()))
	require.NoError(t, err)

	staging := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(staging, "circuit"), content, 0o644))

	cachePath := filepath.Join(t.TempDir(), "circuits.json")
	cache := longfellow.OpenFileCache(cachePath)

	// An entry naming a circuit no build has ever known.
	require.NoError(t, cache.Record(longfellow.FileDigest(content),
		"0000000000000000000000000000000000000000000000000000000000000000"))

	system, err := longfellow.OpenDir(staging, longfellow.WithCache(cache))
	require.NoError(t, err, "a stale entry must not break the load")
	defer system.Close()

	circuits := system.Circuits()
	require.Len(t, circuits, 1)
	require.NotEqual(t, "0000000000000000000000000000000000000000000000000000000000000000",
		circuits[0].Hash)

	// And the wrong entry has been replaced with the real one.
	id, ok := cache.Lookup(longfellow.FileDigest(content))
	require.True(t, ok)
	require.Equal(t, circuits[0].Hash, id)
}

// MapCache is the compiled-in form: a mapping that ships signed with the
// application, so it carries none of a file cache's trust assumption.
func TestMapCacheServesACompiledInMapping(t *testing.T) {
	source := circuitDir(t)
	entries, err := os.ReadDir(source)
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(source, entries[0].Name()))
	require.NoError(t, err)

	staging := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(staging, "circuit"), content, 0o644))

	// What a build step would emit: identify once, then compile the result in.
	discovered := longfellow.OpenFileCache(filepath.Join(t.TempDir(), "circuits.json"))
	system, err := longfellow.OpenDir(staging, longfellow.WithCache(discovered))
	require.NoError(t, err)
	expected := system.Circuits()
	require.NoError(t, system.Close())

	compiled := longfellow.MapCache(discovered.Entries())
	require.Len(t, compiled, 1)

	warm := time.Now()
	cached, err := longfellow.OpenDir(staging, longfellow.WithCache(compiled))
	require.NoError(t, err)
	defer cached.Close()

	require.Equal(t, expected, cached.Circuits())
	t.Logf("compiled-in mapping: loaded in %v", time.Since(warm).Round(time.Millisecond))
}

// A missing or corrupt cache file means "nothing is remembered yet", never a
// refusal to start.
func TestOpenFileCacheToleratesRubbish(t *testing.T) {
	missing := longfellow.OpenFileCache(filepath.Join(t.TempDir(), "nothing.json"))
	require.Empty(t, missing.Entries())

	path := filepath.Join(t.TempDir(), "corrupt.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json at all"), 0o644))
	require.Empty(t, longfellow.OpenFileCache(path).Entries())

	future := filepath.Join(t.TempDir(), "future.json")
	require.NoError(t, os.WriteFile(future, []byte(`{"version":99,"circuits":{"a":"b"}}`), 0o644))
	require.Empty(t, longfellow.OpenFileCache(future).Entries(),
		"an unrecognised format version is treated as empty, not misread")
}

// The two digests over one file are different values and must not be confused:
// FileDigest is a plain SHA-256 of the bytes, the circuit id is SHA-256 over the
// two parsed circuits' own ids.
func TestFileDigestIsNotTheCircuitID(t *testing.T) {
	source := circuitDir(t)
	entries, err := os.ReadDir(source)
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(source, entries[0].Name()))
	require.NoError(t, err)

	staging := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(staging, "circuit"), content, 0o644))

	system, err := longfellow.OpenDir(staging)
	require.NoError(t, err)
	defer system.Close()

	require.NotEqual(t, longfellow.FileDigest(content), system.Circuits()[0].Hash)
}
