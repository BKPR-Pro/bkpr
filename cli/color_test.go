package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// ansiStrip removes SGR sequences and the tabwriter escape byte, leaving what a person actually reads.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func ansiStrip(s string) string {
	return strings.ReplaceAll(ansiRe.ReplaceAllString(s, ""), "\xff", "")
}

func TestColorModeResolvesFlagAndEnv(t *testing.T) {
	cases := []struct {
		mode         string
		tty, noColor bool
		want         bool
	}{
		{"auto", true, false, true},
		{"auto", false, false, false}, // a pipe stays plain
		{"auto", true, true, false},   // NO_COLOR opts out
		{"always", false, true, true}, // an explicit request overrides both
		{"never", true, false, false},
	}
	for _, c := range cases {
		if got := colorMode(c.mode, c.tty, c.noColor); got != c.want {
			t.Errorf("colorMode(%q, tty=%v, noColor=%v) = %v, want %v", c.mode, c.tty, c.noColor, got, c.want)
		}
	}
}

func TestPaintWrapsTextInSGRColorAndReset(t *testing.T) {
	on := tablePalette{on: true}
	got := on.paint(ansiGreen, "1600.00 CAD")
	if !strings.Contains(got, "\x1b[32m") || !strings.Contains(got, "\x1b[0m") {
		t.Errorf("painted cell missing SGR codes: %q", got)
	}
	// Stripping the color must leave exactly the visible text -- nothing added to what a person reads.
	if ansiStrip(got) != "1600.00 CAD" {
		t.Errorf("stripped paint = %q, want the bare text", ansiStrip(got))
	}
	if off := (tablePalette{on: false}).paint(ansiGreen, "x"); off != "x" {
		t.Errorf("color off should return the text unchanged, got %q", off)
	}
}

// The colored table is the plain table with color added: stripping the color must reproduce the plain
// output byte for byte, so alignment and content never depend on whether a person is watching.
func TestColoredReportStripsBackToPlain(t *testing.T) {
	txs, entries := foldBooks(t, booksLog(t))
	sum, err := summarize(txs, entries)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	var plain, colored bytes.Buffer
	if err := report(&plain, txs, entries, sum, false); err != nil {
		t.Fatalf("plain report: %v", err)
	}
	if err := report(&colored, txs, entries, sum, true); err != nil {
		t.Fatalf("colored report: %v", err)
	}

	if strings.Contains(plain.String(), "\x1b[") {
		t.Error("plain report must contain no ANSI codes")
	}
	if !strings.Contains(colored.String(), "\x1b[") {
		t.Error("colored report should contain ANSI codes")
	}
	if ansiStrip(colored.String()) != plain.String() {
		t.Errorf("stripping color must reproduce the plain table exactly\nplain:\n%s\nstripped:\n%s", plain.String(), ansiStrip(colored.String()))
	}
}
