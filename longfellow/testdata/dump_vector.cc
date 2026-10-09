// Dumps one of Google's own mdoc test vectors as text, so a Go test can drive
// the binding over exactly the bytes upstream's mdoc_zk_test.cc drives.
#include <cstdio>
#include <cstring>
#include <cstdlib>

#include "circuits/mdoc/mdoc_examples.h"
#include "circuits/mdoc/mdoc_test_attributes.h"

using namespace proofs;

static void hex(const char* name, const uint8_t* p, size_t n) {
  printf("%s ", name);
  for (size_t i = 0; i < n; i++) printf("%02x", p[i]);
  printf("\n");
}

int main(int argc, char** argv) {
  int idx = argc > 1 ? atoi(argv[1]) : 0;
  const MdocTests& v = mdoc_tests[idx];

  printf("index %d\n", idx);
  printf("pkx %s\n", v.pkx.as_pointer);
  printf("pky %s\n", v.pky.as_pointer);
  printf("doctype %s\n", v.doc_type);
  printf("now %s\n", (const char*)v.now);
  hex("transcript", v.transcript, v.transcript_size);
  hex("mdoc", v.mdoc, v.mdoc_size);

  const RequestedAttribute& a = test::age_over_18;
  hex("attr_namespace", a.namespace_id, a.namespace_len);
  hex("attr_id", a.id, a.id_len);
  hex("attr_value", a.cbor_value, a.cbor_value_len);
  return 0;
}
