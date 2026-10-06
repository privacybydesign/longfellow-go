// Peak-RSS attribution for longfellow's mdoc prover/verifier.
//
// Question: irmago #724's risk table assumes 88 MB resident during proving; the
// Dimensity 8100 measurement saw 445 MB for the whole app process. This asks how
// much of that is the library itself, and which phase owns it.
//
// Samples /proc/self/status every 10 ms from a background thread so the shape of
// the curve is visible, not just the peak.
#include <atomic>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fstream>
#include <string>
#include <thread>
#include <vector>

#include "circuits/mdoc/mdoc_zk.h"
#include "circuits/mdoc/mdoc_examples.h"
#include "circuits/mdoc/mdoc_test_attributes.h"

static long field(const char* key) {
  std::ifstream f("/proc/self/status");
  std::string line;
  size_t klen = strlen(key);
  while (std::getline(f, line)) {
    if (line.compare(0, klen, key) == 0) {
      return atol(line.c_str() + klen);
    }
  }
  return -1;
}
static long rss() { return field("VmRSS:"); }
static long hwm() { return field("VmHWM:"); }

static std::atomic<bool> sampling{true};
static std::vector<std::pair<long, long>> trace;  // ms since start, RSS kB
static std::chrono::steady_clock::time_point t0;

static void sampler() {
  while (sampling.load()) {
    long ms = std::chrono::duration_cast<std::chrono::milliseconds>(
                  std::chrono::steady_clock::now() - t0).count();
    trace.emplace_back(ms, rss());
    std::this_thread::sleep_for(std::chrono::milliseconds(10));
  }
}

static void mark(const char* label) {
  printf("%-34s RSS=%7ld kB  HWM=%7ld kB\n", label, rss(), hwm());
  fflush(stdout);
}

static const ZkSpecStruct* find_spec(size_t version, size_t nattr) {
  for (size_t i = 0; i < kNumZkSpecs; i++) {
    if (kZkSpecs[i].version == version && kZkSpecs[i].num_attributes == nattr) {
      return &kZkSpecs[i];
    }
  }
  return nullptr;
}

int main(int argc, char** argv) {
  // Usage rather than a crash on no arguments: run-on-phone.ps1 execs this with
  // none as a pre-flight check that the device allows exec from
  // /data/local/tmp, and an abort there reads as a failure when it is not.
  if (argc < 2) {
    fprintf(stderr, "usage: mem_probe <circuit-file> [version] [attrs] [trace.csv]\n");
    return 2;
  }
  const char* circuit_path = argv[1];
  size_t version = argc > 2 ? atoi(argv[2]) : 6;
  size_t nattr = argc > 3 ? atoi(argv[3]) : 1;
  const char* trace_path = argc > 4 ? argv[4] : nullptr;

  const ZkSpecStruct* spec = find_spec(version, nattr);
  if (!spec) { fprintf(stderr, "no spec v%zu/%zu attrs\n", version, nattr); return 1; }
  printf("spec: %s version=%zu attrs=%zu\n", spec->system, spec->version, spec->num_attributes);

  t0 = std::chrono::steady_clock::now();
  std::thread sth(sampler);

  mark("start");

  // ---- circuit bytes (compressed, as shipped) ----
  std::vector<uint8_t> circuit;
  {
    std::ifstream f(circuit_path, std::ios::binary);
    if (!f) { fprintf(stderr, "cannot read %s\n", circuit_path); return 1; }
    circuit.assign(std::istreambuf_iterator<char>(f), std::istreambuf_iterator<char>());
  }
  printf("circuit file: %zu bytes\n", circuit.size());
  mark("after reading circuit file");

  // ---- attributes ----
  // Upstream's own vectors: case 0 is the single-claim mDL, case 3 the one its
  // two_claims test uses. Requesting more attributes needs a bigger circuit AND
  // an mdoc that actually carries them, so the pair moves together.
  const proofs::MdocTests& T = proofs::mdoc_tests[nattr == 1 ? 0 : 3];
  RequestedAttribute attrs[2] = {proofs::test::age_over_18, proofs::test::familyname_mustermann};

  // ---- prove ----
  uint8_t* proof = nullptr; size_t proof_len = 0;
  auto ps = std::chrono::steady_clock::now();
  MdocProverErrorCode rc = run_mdoc_prover(
      circuit.data(), circuit.size(), T.mdoc, T.mdoc_size,
      (const char*)T.pkx.as_pointer, (const char*)T.pky.as_pointer,
      T.transcript, T.transcript_size, attrs, nattr, (const char*)T.now, &proof, &proof_len, spec);
  long prove_ms = std::chrono::duration_cast<std::chrono::milliseconds>(
      std::chrono::steady_clock::now() - ps).count();
  if (rc != MDOC_PROVER_SUCCESS) { fprintf(stderr, "prover failed: %d\n", rc); return 1; }
  printf("prove: %zu byte proof in %ld ms\n", proof_len, prove_ms);
  mark("after prove");

  // ---- verify ----
  auto vs = std::chrono::steady_clock::now();
  MdocVerifierErrorCode vrc = run_mdoc_verifier(
      circuit.data(), circuit.size(),
      (const char*)T.pkx.as_pointer, (const char*)T.pky.as_pointer,
      T.transcript, T.transcript_size, attrs, nattr, (const char*)T.now, proof, proof_len,
      (const char*)T.doc_type, spec);
  long verify_ms = std::chrono::duration_cast<std::chrono::milliseconds>(
      std::chrono::steady_clock::now() - vs).count();
  if (vrc != MDOC_VERIFIER_SUCCESS) { fprintf(stderr, "verifier failed: %d\n", vrc); return 1; }
  printf("verify: OK in %ld ms\n", verify_ms);
  mark("after verify");

  free(proof);
  sampling.store(false);
  sth.join();

  long peak = 0;
  for (auto& p : trace) peak = std::max(peak, p.second);
  printf("\npeak sampled RSS: %ld kB (%.1f MB)   VmHWM: %ld kB (%.1f MB)\n",
         peak, peak / 1024.0, hwm(), hwm() / 1024.0);

  if (trace_path) {
    FILE* f = fopen(trace_path, "w");
    fprintf(f, "ms,rss_kb\n");
    for (auto& p : trace) fprintf(f, "%ld,%ld\n", p.first, p.second);
    fclose(f);
    printf("trace -> %s (%zu samples)\n", trace_path, trace.size());
  }
  return 0;
}
