package longfellow

// The library's logger defaults to INFO, at which proving and verifying print
// timing lines to stderr (logcat on Android). They are turned down to ERROR
// before anything else in this package can run — see quiet.cc for why that
// takes a C++ shim rather than a line in the binding's preamble.

// void longfellow_go_quiet_logging(void);
import "C"

func init() {
	C.longfellow_go_quiet_logging()
}
