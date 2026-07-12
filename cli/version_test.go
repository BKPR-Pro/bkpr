package main

import (
	"bytes"
	"strings"
	"testing"
)

// versionLine renders the display string from its parts. The revision is
// abbreviated to a git-short length so a release line stays readable.
func TestVersionLineAbbreviatesTheRevision(t *testing.T) {
	got := versionLine("v1.2.0", "abcdef1234567890", "")
	if want := "bk v1.2.0 (abcdef123456)"; got != want {
		t.Errorf("versionLine = %q, want %q", got, want)
	}
}

// A build with no VCS revision (the version alone is enough to identify it)
// omits the parenthetical rather than printing empty parentheses.
func TestVersionLineOmitsAnEmptyRevision(t *testing.T) {
	got := versionLine("v1.2.0", "", "")
	if want := "bk v1.2.0"; got != want {
		t.Errorf("versionLine = %q, want %q", got, want)
	}
}

// A dirty checkout carries the modified marker inside the parenthetical.
func TestVersionLineShowsTheModifiedMarker(t *testing.T) {
	got := versionLine("(devel)", "abc123", ", modified")
	if want := "bk (devel) (abc123, modified)"; got != want {
		t.Errorf("versionLine = %q, want %q", got, want)
	}
}

// A release binary is built with -ldflags "-X main.version=<tag>", and that
// injected tag must win over the (devel) the toolchain records for a plain
// `go build` from a checkout.
func TestVersionCmdPrefersTheInjectedVersion(t *testing.T) {
	saved := version
	version = "v9.9.9"
	defer func() { version = saved }()

	var buf bytes.Buffer
	versionCmd(&buf)
	if got := buf.String(); !strings.HasPrefix(got, "bk v9.9.9") {
		t.Errorf("versionCmd output = %q, want it to start with %q", got, "bk v9.9.9")
	}
}
