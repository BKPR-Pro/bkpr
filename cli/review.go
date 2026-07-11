package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

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

// review prints the decision queue: the lines the rules could not place, each with the fingerprint
// a correction is keyed by. The table is for a person; -format json is the surface an external
// model reads. It never writes; the answers come back through categorize and rules set.
func review(args []string) error {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	format := fs.String("format", "table", "output format: table for a person, json for a model")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	rep, err := reviewData(s.Log)
	if err != nil {
		return err
	}

	switch *format {
	case "table":
		return reviewTable(os.Stdout, rep)
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	default:
		return fmt.Errorf("unknown format %q: want table or json", *format)
	}
}

// reviewTable renders the queue for a person: the fingerprint first, because it is the handle every
// correction takes, and a closing line that says what to do with one.
func reviewTable(out io.Writer, rep reviewReport) error {
	if len(rep.Uncategorized) == 0 {
		fmt.Fprintln(out, "nothing to review: every line is categorized")
		return nil
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FINGERPRINT\tDATE\tACCOUNT\tAMOUNT\tDESCRIPTION\tPOSTS TO")
	for _, item := range rep.Uncategorized {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			item.Fingerprint, item.Date, item.Account, item.Amount, item.Description,
			strings.Join(item.PostsTo, " + "))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(out, "\n%d lines to place: categorize -tx <fingerprint> answers one, rules set answers every line like it\n",
		len(rep.Uncategorized))
	return nil
}
