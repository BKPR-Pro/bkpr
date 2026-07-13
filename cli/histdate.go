package main

import (
	"fmt"
	"strings"
	"time"
)

// histDateLayouts are the ways a person may write a -from/-to date, tried in turn. Go parses month
// names case-insensitively, so "feb" and "February" both work.
var histDateLayouts = []string{
	"2006-01-02",
	"Jan 2, 2006",
	"January 2, 2006",
	"Jan 2 2006",
	"1/2/2006",
	"01/02/2006",
}

// parseHistDate normalizes a written date to "MMM D, YYYY", the form the bank's date filter reads.
func parseHistDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	for _, layout := range histDateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("Jan 2, 2006"), nil
		}
	}
	return "", fmt.Errorf("import: could not read the date %q; try 2026-02-01 or \"Feb 1, 2026\"", s)
}

// parseAsOf reads a written date into a time, accepting the same forms -from/-to do. It is what
// `balance set -as-of` uses to date a hand-recorded balance.
func parseAsOf(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range histDateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("could not read the date %q; try 2026-02-01 or \"Feb 1, 2026\"", s)
}

// backfillRange resolves the -from/-to flags into a normalized date pair. An empty from means no
// explicit range (the relative -history is used instead). A from with no to reads up to today. A to
// without a from is refused, since a range needs a start.
func backfillRange(fromFlag, toFlag string) (from, to string, err error) {
	if strings.TrimSpace(fromFlag) == "" {
		if strings.TrimSpace(toFlag) != "" {
			return "", "", fmt.Errorf("import: -to needs -from (a range needs a start date)")
		}
		return "", "", nil
	}
	if from, err = parseHistDate(fromFlag); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(toFlag) == "" {
		return from, time.Now().Format("Jan 2, 2006"), nil
	}
	if to, err = parseHistDate(toFlag); err != nil {
		return "", "", err
	}
	return from, to, nil
}
