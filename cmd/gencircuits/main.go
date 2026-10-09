// gencircuits writes every circuit the pinned library is able to emit into a
// directory, so a machine with no circuits of its own can run the proving tests.
//
// It exists because of an asymmetry that is structural rather than a quirk:
// generate_circuit "only supports the latest version of the ZKSpec for a number
// of attributes. Attempt to generate older circuits will result in an error."
// So what this produces is whatever revision the pinned longfellow-zk commit
// considers newest, and every older one -- v6 included, the revision the
// captured EUDI AV reader offered -- cannot be produced from source at all.
// Readers reasonably lag the library, so that is the normal case, and it is why
// LONGFELLOW_CIRCUITS points at a directory instead of being generated.
//
// What a run against this output therefore is: the whole prover, verifier,
// adapter and session path exercised against circuits whose only provenance is
// the pinned source. What it is NOT: a stand-in for the circuit directory. A
// caller says which it has by setting LONGFELLOW_CIRCUITS_GENERATED, and the
// tests that need a revision this cannot emit then skip rather than fail.
//
// Each file is named the way Multipaz names its circuits, for familiarity only.
// Nothing reads these names -- Open identifies a circuit by recomputing
// circuit_id over its bytes. This checks that the two agree, which is the one
// thing a generator can establish and a loader cannot: a loader sees only what
// it was handed, while this knows which spec it asked for.
//
// Needs the native library, so it runs in the build container:
//
//	docker run --rm -v <out>:/circuits longfellow-build generate-circuits.sh /circuits
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/privacybydesign/irmago/eudi/credentials/mdoc/zk"
	"github.com/privacybydesign/longfellow-go/longfellow"
)

func main() {
	out := flag.String("out", "", "directory to write circuits into")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: gencircuits -out DIR")
		os.Exit(2)
	}
	if err := run(*out); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
}

func run(out string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	specs := longfellow.LibrarySpecs()
	if len(specs) == 0 {
		return fmt.Errorf("the library reports no circuit specs at all")
	}

	// Newest first, so the log reads as "these worked, and these are older than
	// the generator and never could have".
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Version != specs[j].Version {
			return specs[i].Version > specs[j].Version
		}
		return specs[i].NumAttributes < specs[j].NumAttributes
	})

	var written, refused []zk.Circuit
	for _, spec := range specs {
		circuit, err := longfellow.GenerateCircuit(spec.Hash)
		if err != nil {
			// Not a failure. Every revision but the newest refuses by design,
			// and that refusal is the whole reason this cannot stand in for a
			// real circuit directory.
			refused = append(refused, spec)
			fmt.Printf("  skip  v%d/%d attr: %v\n", spec.Version, spec.NumAttributes, err)
			continue
		}

		name := filename(spec)
		if err := os.WriteFile(filepath.Join(out, name), circuit, 0o644); err != nil {
			return err
		}
		written = append(written, spec)
		fmt.Printf("  wrote v%d/%d attr: %s (%d bytes)\n",
			spec.Version, spec.NumAttributes, name, len(circuit))
	}

	if len(written) == 0 {
		return fmt.Errorf("the library emitted no circuits at all: %d specs, every one refused", len(refused))
	}
	if err := verify(out, written); err != nil {
		return err
	}

	fmt.Printf("\n%d circuits in %s\n", len(written), out)
	if versions := missingVersions(refused, written); len(versions) > 0 {
		fmt.Printf("NOT COVERED by a run against this directory: %s\n", strings.Join(versions, ", "))
		fmt.Println("The generator emits only the library's newest revision; older ones are unobtainable from source.")
	}

	// Deliberately an environment variable rather than a marker file in the
	// directory. Open itself would skip a dotfile, but not everything that
	// reads a circuit directory goes through Open: the tests pick a file with
	// os.ReadDir and copy entries[0], so a marker sorting first was copied AS a
	// circuit and broke five of them. A circuit directory holds circuits.
	fmt.Println("\nSet LONGFELLOW_CIRCUITS_GENERATED=1 alongside LONGFELLOW_CIRCUITS,")
	fmt.Println("so tests needing a revision this cannot emit skip rather than fail.")
	return nil
}

// filename follows Multipaz's convention. It is decoration: the loader never
// consults it.
func filename(spec zk.Circuit) string {
	return fmt.Sprintf("%d_%d_%d_%d_%s",
		spec.Version, spec.NumAttributes, spec.BlockEncHash, spec.BlockEncSig, spec.Hash)
}

// verify re-opens what was written through the ordinary loading path, which
// recomputes circuit_id over the bytes and looks the result up in the library's
// table. What it establishes is narrow and worth having: that the bytes
// generate_circuit returned for a spec really are the circuit that spec names.
func verify(dir string, written []zk.Circuit) error {
	system, err := longfellow.OpenDir(dir)
	if err != nil {
		return fmt.Errorf("the generated directory does not load: %w", err)
	}
	defer system.Close()

	loaded := map[string]bool{}
	for _, circuit := range system.Circuits() {
		loaded[circuit.Hash] = true
	}
	for _, spec := range written {
		if !loaded[spec.Hash] {
			return fmt.Errorf("v%d/%d attr: the generated bytes do not identify as %s",
				spec.Version, spec.NumAttributes, spec.Hash)
		}
	}

	fmt.Printf("verified: all %d circuits load under the id their spec claims\n", len(written))
	return nil
}

// missingVersions names the revisions no file here covers. A revision that was
// refused for one attribute count but written for another is not missing.
func missingVersions(refused, written []zk.Circuit) []string {
	present := map[int]bool{}
	for _, spec := range written {
		present[spec.Version] = true
	}

	seen := map[int]bool{}
	var versions []int
	for _, spec := range refused {
		if present[spec.Version] || seen[spec.Version] {
			continue
		}
		seen[spec.Version] = true
		versions = append(versions, spec.Version)
	}
	sort.Ints(versions)

	out := make([]string, 0, len(versions))
	for _, v := range versions {
		out = append(out, fmt.Sprintf("v%d", v))
	}
	return out
}
