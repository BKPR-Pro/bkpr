package main

import (
	"encoding/json"
	"os"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/store"
)

// reviewPosting is one categorized posting: an account, its amount, and a price when the amount is in
// another commodity (a share bought with cash).
type reviewPosting struct {
	Account string `json:"account"`
	Amount  string `json:"amount"`
	Cost    string `json:"cost,omitempty"`
}

// reviewEntry is one transaction and the entry it currently folds to, carrying the fingerprint an
// authoring command writes back against. An unplaced line is not singled out: its postings name an
// account ending in Uncategorized, the same marker `ledger bal Uncategorized` reads, so the reader
// finds what it cares about rather than being handed a curated subset.
type reviewEntry struct {
	Fingerprint string          `json:"fingerprint"`
	Date        string          `json:"date"`
	Account     string          `json:"account"`
	Amount      string          `json:"amount"`
	Description string          `json:"description"`
	Payee       string          `json:"payee"`
	Postings    []reviewPosting `json:"postings"`
}

// reviewReport is the whole books rendered for a machine: every entry, in date order, the same fold
// the table and the ledger artifact render, just as JSON.
type reviewReport struct {
	Entries []reviewEntry `json:"entries"`
}

// reviewData folds the books into every entry. It has no side effect, so it is the pure core the
// command and the tests share.
func reviewData(log *eventlog.Log) (reviewReport, error) {
	txs, entries, err := books.Ledger(log)
	if err != nil {
		return reviewReport{}, err
	}

	rep := reviewReport{Entries: make([]reviewEntry, 0, len(txs))}
	for i, tx := range txs {
		rep.Entries = append(rep.Entries, reviewEntry{
			Fingerprint: tx.ID,
			Date:        tx.Date.Format("2006-01-02"),
			Account:     tx.Account,
			Amount:      tx.Amount.String(),
			Description: tx.Description,
			Payee:       entries[i].Payee,
			Postings:    reviewPostings(entries[i]),
		})
	}
	return rep, nil
}

func reviewPostings(e model.Entry) []reviewPosting {
	out := make([]reviewPosting, len(e.Postings))
	for i, p := range e.Postings {
		out[i] = reviewPosting{Account: p.Account, Amount: p.Amount.String()}
		if p.Cost != nil {
			out[i].Cost = p.Cost.String()
		}
	}
	return out
}

// review prints the whole books as JSON, in date order: a generic report of every entry, the same
// fold `books` renders as a table. It never writes; the reader answers back through categorize and
// rules set, and finds unplaced lines by their Uncategorized account, not a special queue.
func review(args []string) error {
	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	rep, err := reviewData(s.Log)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}
