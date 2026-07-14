package source

import (
	"os"
	"testing"
)

// requireBrowserTests gates the tests that drive a real browser through Node and Playwright. Those
// tests launch Chromium against a local fixture server -- and a bank behind a bot manager (Simplii)
// takes the stealth path, which is headed and pops a window that cannot be hidden on macOS -- so a
// plain `go test ./...` skips them. Set BK_BROWSER_TESTS=1 to run them, which you want after changing
// a bank's script or its selectors:
//
//	BK_BROWSER_TESTS=1 go test ./lib/adapters/source/
//
// They stay compiled either way, so a change to the Bank shape or execBankScript still type-checks
// them on every run; only their execution is gated.
func requireBrowserTests(t *testing.T) {
	t.Helper()
	if os.Getenv("BK_BROWSER_TESTS") != "1" {
		t.Skip("skipping browser-driving test; set BK_BROWSER_TESTS=1 to run (needs Node + Playwright, and opens a browser)")
	}
}
