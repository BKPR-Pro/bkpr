package main

import "testing"

// parseHistDate accepts the common ways a person writes a date and normalizes to the "MMM D, YYYY"
// the bank's filter reads, so -from "2026-02-01" and -from "feb 1, 2026" mean the same thing.
func TestParseHistDate(t *testing.T) {
	cases := map[string]string{
		"2026-02-01":       "Feb 1, 2026",
		"Feb 1, 2026":      "Feb 1, 2026",
		"feb 1, 2026":      "Feb 1, 2026",
		"February 1, 2026": "Feb 1, 2026",
		"2/1/2026":         "Feb 1, 2026",
		" 2026-02-01 ":     "Feb 1, 2026",
	}
	for in, want := range cases {
		got, err := parseHistDate(in)
		if err != nil {
			t.Errorf("parseHistDate(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseHistDate(%q) = %q, want %q", in, got, want)
		}
	}

	if _, err := parseHistDate("not a date"); err == nil {
		t.Error("parseHistDate should reject an unparseable date")
	}
}

func TestBackfillRange(t *testing.T) {
	// No -from: no explicit range (relative -history is used instead).
	if from, to, err := backfillRange("", ""); err != nil || from != "" || to != "" {
		t.Errorf("backfillRange(\"\",\"\") = (%q,%q,%v), want empties", from, to, err)
	}

	// -from with no -to reads up to today.
	from, to, err := backfillRange("2026-02-01", "")
	if err != nil || from != "Feb 1, 2026" || to == "" {
		t.Errorf("backfillRange from-only = (%q,%q,%v)", from, to, err)
	}

	// Both given are normalized.
	from, to, err = backfillRange("feb 1, 2026", "june 1, 2026")
	if err != nil || from != "Feb 1, 2026" || to != "Jun 1, 2026" {
		t.Errorf("backfillRange both = (%q,%q,%v)", from, to, err)
	}

	// -to without -from is refused.
	if _, _, err := backfillRange("", "june 1, 2026"); err == nil {
		t.Error("backfillRange should refuse -to without -from")
	}
}
