package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// Every command in the usage block answers to help, so nobody scrolls docs to find a flag.
func TestHelpTopicKnowsEveryCommand(t *testing.T) {
	for _, name := range []string{
		"init", "connectors", "rules", "import", "categorize", "void",
		"match", "review", "export", "books", "invoice", "bill",
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
