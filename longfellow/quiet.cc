// Silences the longfellow library's own logger.
//
// The library logs through proofs::log (util/log.cc) at a process-global level
// that defaults to INFO — at which every prove and verify prints timing lines
// ("[INFO][+  ...ms] ZK sumcheck done") to stderr, or to logcat on Android.
// Harmless in a test harness, wrong in a wallet, and not a decision a library
// embedded in one gets to make.
//
// set_log_level is C++ in namespace proofs and is not part of the installed C
// ABI — the install tree ships mdoc_zk.h alone — so it cannot be declared in a
// cgo preamble. The declarations below mirror util/log.h at the pinned commit,
// which makes the mangled name resolve against libmdoc_static.a; should the
// enum or the signature ever change upstream, this fails at link time rather
// than drifting silently. Upstream's own circuit_maker.cc makes exactly this
// call.
//
// ERROR rather than full silence: the enum has nothing below ERROR, and a
// library error is the one thing worth hearing about.

namespace proofs {
enum LogLevel {
  ERROR = 1,
  WARNING = 10,
  INFO = 100,
};
void set_log_level(enum LogLevel l);
}  // namespace proofs

extern "C" void longfellow_go_quiet_logging(void) {
  proofs::set_log_level(proofs::ERROR);
}
