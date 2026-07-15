package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// The ASCII wordmark's rows must all be one width, or the banner leans; a hand-set banner is easy
// to knock out of true, so the alignment is pinned by a test.
func TestWordmarkRowsAreAligned(t *testing.T) {
	lines := strings.Split(wordmark, "\n")
	want := utf8.RuneCountInString(lines[0])
	for _, l := range lines {
		if got := utf8.RuneCountInString(l); got != want {
			t.Errorf("wordmark row %q is %d wide, want %d", l, got, want)
		}
	}
}

// The usage screen stands on its own vocabulary rather than leaning on a git analogy.
func TestUsageDoesNotMentionGit(t *testing.T) {
	var buf bytes.Buffer
	writeUsage(&buf, palette{})
	if strings.Contains(strings.ToLower(buf.String()), "git") {
		t.Error("the usage screen should not mention git")
	}
}

// The masthead shows the ASCII wordmark over a tagline, and a plain palette leaves it bare so a pipe
// gets clean text.
func TestMastheadShowsWordmarkAndTagline(t *testing.T) {
	plain := masthead(palette{})
	if !strings.Contains(plain, wordmark) {
		t.Error("the masthead should carry the ASCII wordmark")
	}
	if !strings.Contains(plain, "bookkeeper") {
		t.Error("the masthead should show the bookkeeper tagline")
	}
	if strings.Contains(plain, "\x1b") {
		t.Error("a plain masthead must carry no ANSI")
	}
	if !strings.Contains(masthead(colorPalette), colorPalette.heading) {
		t.Error("a color masthead should paint the wordmark")
	}
}

// The styled usage screen groups every command under a section heading and paints the verb, so a
// person scanning it lands on the right command without reading prose.
func TestUsageStylesEveryCommandUnderASection(t *testing.T) {
	var buf bytes.Buffer
	writeUsage(&buf, colorPalette)
	out := buf.String()
	if !strings.Contains(out, "\x1b[") {
		t.Fatal("a color palette should paint the usage screen with ANSI styling")
	}
	for _, name := range []string{
		"init", "reset", "migrate", "connectors register", "rules set", "import", "categorize", "void",
		"match", "export", "books", "register", "invoice raise", "bill receive", "policy set",
		"accounts set", "balance set", "reconcile", "receipt", "report", "help", "docs", "version",
	} {
		if !strings.Contains(out, name) {
			t.Errorf("usage should list the %q command", name)
		}
	}
	for _, title := range []string{"SETUP", "RULES", "BOOKKEEPING", "INVOICES AND BILLS", "POLICIES AND DOCUMENTS"} {
		if !strings.Contains(out, title) {
			t.Errorf("usage should carry the %q section header", title)
		}
	}
}

// The rules help topic documents the tax split, so a person authoring a taxed vendor learns the two
// flags from the tool itself, not only the README.
func TestRulesHelpDocumentsTax(t *testing.T) {
	var buf bytes.Buffer
	if err := helpTopic(&buf, "rules"); err != nil {
		t.Fatalf("helpTopic: %v", err)
	}
	for _, want := range []string{"-tax-rate", "-tax-account"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("rules help should mention %q", want)
		}
	}
}

// The books help topic documents valuing a mixed-commodity book into one currency, so a person with
// USD income learns the two flags from the tool itself.
func TestBooksHelpDocumentsValue(t *testing.T) {
	var buf bytes.Buffer
	if err := helpTopic(&buf, "books"); err != nil {
		t.Fatalf("helpTopic: %v", err)
	}
	for _, want := range []string{"-value", "-rate"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("books help should mention %q", want)
		}
	}
}

// A plain palette leaves the text bare, so a pipe, a redirect, or an agent reading the screen never
// sees an escape code.
func TestUsagePlainPaletteEmitsNoAnsi(t *testing.T) {
	var buf bytes.Buffer
	writeUsage(&buf, palette{})
	if strings.Contains(buf.String(), "\x1b") {
		t.Error("a plain palette must not emit ANSI escapes")
	}
}

// Optional [ ... ] groups are dimmed so the required arguments read as the ones that stand out; the
// required arguments ahead of them are left bright.
func TestDimOptionalsDimsOnlyBracketedGroups(t *testing.T) {
	got := dimOptionals("<re> [-why <reason>]", colorPalette)
	if !strings.Contains(got, colorPalette.dim+"[-why <reason>]") {
		t.Errorf("the optional group should be dimmed: %q", got)
	}
	if strings.Contains(got[:strings.Index(got, "[")], colorPalette.dim) {
		t.Errorf("the required argument ahead of the optional should stay bright: %q", got)
	}
}

// NO_COLOR wins over the terminal check, so the widely-honored opt-out silences styling even at a
// real keyboard.
func TestPaletteForHonorsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if paletteFor(os.Stdout) != (palette{}) {
		t.Error("NO_COLOR should disable styling regardless of the terminal")
	}
}

// Every command in the usage block answers to help, so nobody scrolls docs to find a flag.
func TestHelpTopicKnowsEveryCommand(t *testing.T) {
	for _, name := range []string{
		"init", "connectors", "rules", "import", "categorize", "void",
		"match", "export", "books", "register", "invoice", "bill",
	} {
		var buf bytes.Buffer
		if err := helpTopic(&buf, name); err != nil {
			t.Errorf("help %s: %v", name, err)
		}
		if !strings.Contains(buf.String(), name) {
			t.Errorf("help %s should mention the command:\n%s", name, buf.String())
		}
	}
}

func TestHelpTopicRefusesAnUnknownCommand(t *testing.T) {
	if err := helpTopic(io.Discard, "frobnicate"); err == nil {
		t.Error("an unknown topic should be refused, pointing at docs")
	}
}

// docs is built from the same sections help reads, so the reference stays whole: every topic's
// text appears in the full printout.
func TestDocsPrintsEveryTopic(t *testing.T) {
	var buf bytes.Buffer
	docs(&buf)
	out := buf.String()
	for _, g := range reference {
		if !strings.Contains(out, g.title) {
			t.Errorf("docs should carry the %q heading", g.title)
		}
		for _, topic := range g.topics {
			if !strings.Contains(out, topic.text) {
				t.Errorf("docs should carry the block for %v", topic.names)
			}
		}
	}
}

// reclassified is the count a rule edit reports: lines whose entries changed, appeared, or left
// between two folds of the books.
func TestReclassifiedCountsChangedAddedAndRemovedLines(t *testing.T) {
	was := map[string]string{"a": "x", "b": "y", "c": "z"}
	now := map[string]string{"a": "x", "b": "Y", "d": "w"}
	// b changed, c left, d appeared; a is untouched.
	if got := reclassified(was, now); got != 3 {
		t.Errorf("reclassified = %d, want 3", got)
	}
	if got := reclassified(was, was); got != 0 {
		t.Errorf("an identical fold should count 0, got %d", got)
	}
}
