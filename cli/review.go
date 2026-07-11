package main

import (
	"encoding/json"
	"os"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/store"
)

// reviewItem is one line waiting on a decision, with the fingerprint a model writes back against and
// enough of the line to decide. PostsTo is where it currently lands, so a bare Uncategorized (the
// kind is unknown) reads apart from a leaf like Expenses:Materials:Uncategorized (only the detail).
type reviewItem struct {
	Fingerprint string   `json:"fingerprint"`
	Date        string   `json:"date"`
	Account     string   `json:"account"`
	Amount      string   `json:"amount"`
	Description string   `json:"description"`
	Payee       string   `json:"payee"`
	PostsTo     []string `json:"posts_to"`
}

// reviewReport is the machine-readable queue an external model drives from. It is deliberately just
// the open decisions, not the whole books, so the model reads only what is unresolved.
type reviewReport struct {
	Uncategorized []reviewItem `json:"uncategorized"`
}

// reviewData folds the books and collects the lines the rules could not place. It has no side
// effect, so it is the pure core the command and the tests share.
func reviewData(log *eventlog.Log) (reviewReport, error) {
	txs, entries, err := books.Ledger(log)
	if err != nil {
		return reviewReport{}, err
	}

	var rep reviewReport
	for i, tx := range txs {
		if !entries[i].Uncategorized() {
			continue
		}
		rep.Uncategorized = append(rep.Uncategorized, reviewItem{
			Fingerprint: tx.ID,
			Date:        tx.Date.Format("2006-01-02"),
			Account:     tx.Account,
			Amount:      tx.Amount.String(),
			Description: tx.Description,
			Payee:       entries[i].Payee,
			PostsTo:     postingAccounts(entries[i]),
		})
	}
	return rep, nil
}

func postingAccounts(e model.Entry) []string {
	out := make([]string, len(e.Postings))
	for i, p := range e.Postings {
		out[i] = p.Account
	}
	return out
}

// review prints the decision queue as JSON, the surface an external model reads to know what needs
// categorizing. It never writes; the model answers back through categorize and rules set.
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
