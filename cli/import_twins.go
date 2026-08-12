package main

import (
	"fmt"
	"io"
	"os"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/eventlog"
)

// twinsAmong reports the candidate twins that the lines just landed are part of: the same date and
// amount as a line already in the book. It is the register's own -dups grouping, aimed at one import's
// worth of lines instead of the whole ledger.
//
// An import is the moment a duplicate is created and, until now, the moment nothing looked for one.
// The fingerprint dedupe only holds while a memo is stable, so a bank that rewrites how it prints a
// line it already served -- "Online Banking payment" arriving later as "Online Banking payment - 7604
// PROV NB PROP TX" -- lands it twice through the same door, and reconcile cannot see it either when the
// dates fall inside a derived opening balance. Both of those happened on one book in one evening: 110
// duplicated charges on one card, five more on the chequing, every run reporting a clean seam.
func twinsAmong(log *eventlog.Log, landed []string) ([]twinGroup, error) {
	if len(landed) == 0 {
		return nil, nil
	}
	fresh := make(map[string]bool, len(landed))
	for _, id := range landed {
		fresh[id] = true
	}

	txs, entries, err := books.Ledger(log)
	if err != nil {
		return nil, err
	}
	doors, err := books.Doors(log)
	if err != nil {
		return nil, err
	}
	settlements, err := books.Settlements(log)
	if err != nil {
		return nil, err
	}

	var out []twinGroup
	for _, g := range twinGroups(txs, entries, doors, settlements) {
		for _, row := range g.Rows {
			if fresh[row.ID] {
				out = append(out, g)
				break
			}
		}
	}
	return out, nil
}

// reportTwinsForIDs runs twinsAmong for the given ids and prints what it finds, silent when it finds
// nothing. Both the import path (checking the lines an import just landed) and the categorize path
// (checking the one line just categorized) share this: a duplicate does not care whether it was
// created by an import serving a line twice or by hand-categorizing an old twin nobody noticed.
func reportTwinsForIDs(log *eventlog.Log, ids []string) {
	groups, err := twinsAmong(log, ids)
	if err != nil {
		return
	}
	warnTwins(os.Stdout, groups)
}

// warnTwins prints what twinsAmong found, and nothing at all when it finds nothing -- a warning on
// every clean run is a warning nobody reads. It only ever suggests: a same-day, same-amount repeat is
// something a bank legitimately produces, so the reader decides, and the full picture is one command
// away.
func warnTwins(w io.Writer, groups []twinGroup) {
	if len(groups) == 0 {
		return
	}
	twinLines, nearLines := 0, 0
	for _, g := range groups {
		if g.Kind == "near" {
			nearLines += len(g.Rows) - 1
		} else {
			twinLines += len(g.Rows) - 1
		}
	}
	// A near-date, same-payee, same-amount match is a stronger duplicate signal than a same-day
	// collision -- a bank reposting a payment a day or two later looks nothing like a coincidence --
	// so it is reported first and louder.
	if nearLines > 0 {
		noun, verb := "lines", "match"
		if nearLines == 1 {
			noun, verb = "line", "matches"
		}
		fmt.Fprintf(w, "WARNING: %d imported %s likely a duplicate: %s a line already in the book on "+
			"payee and amount, dated within %d days;\nreview with bkpr register -dups before treating "+
			"this import as clean\n", nearLines, noun, verb, nearDateTolerance)
	}
	if twinLines > 0 {
		noun, verb := "lines", "match"
		if twinLines == 1 {
			noun, verb = "line", "matches"
		}
		fmt.Fprintf(w, "warning: %d imported %s %s a line already in the book on date and amount;\n"+
			"review with bkpr register -dups before treating this import as clean\n", twinLines, noun, verb)
	}
}
