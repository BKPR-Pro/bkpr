package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dallasread/bookkeeper/lib/adapters/ledger"
	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/store"
)

// renderBooks folds the log and renders it: a table or JSON to read, or the ledger artifact.
// -account narrows the reading to the lines posting to a matching account, so there is no separate
// review command: the decision queue is `books -account Uncategorized`, and any other account
// question is the same machinery with a different pattern.
func renderBooks(args []string) error {
	fs := flag.NewFlagSet("books", flag.ExitOnError)
	format := fs.String("format", "table", "output format: table, json, or ledger")
	basis := fs.String("basis", "cash", "accounting basis: cash or accrual")
	since := fs.String("since", "", "on -basis accrual, book only invoices/bills dated on or after this (YYYY-MM-DD)")
	account := fs.String("account", "", "show only lines posting to an account matching this pattern, e.g. Uncategorized")
	stdout := fs.Bool("stdout", false, "write the ledger to stdout instead of the store")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *basis != string(books.CashBasis) && *basis != string(books.AccrualBasis) {
		return fmt.Errorf("unknown basis %q: want cash or accrual", *basis)
	}
	var effective time.Time
	if *since != "" {
		if *basis != string(books.AccrualBasis) {
			return fmt.Errorf("-since only applies to -basis accrual")
		}
		var err error
		if effective, err = time.Parse("2006-01-02", *since); err != nil {
			return fmt.Errorf("-since %q is not YYYY-MM-DD", *since)
		}
	}
	if *account != "" && *format == "ledger" {
		return fmt.Errorf("-account narrows a reading; the ledger artifact is always whole")
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	txs, entries, err := books.LedgerBasisSince(s.Log, books.Basis(*basis), effective)
	if err != nil {
		return err
	}
	if *account != "" {
		if txs, entries, err = filterByAccount(*account, txs, entries); err != nil {
			return err
		}
	}

	switch *format {
	case "table":
		return report(os.Stdout, txs, entries)
	case "json":
		return writeJSON(os.Stdout, txs, entries)
	case "ledger":
		if *stdout {
			return ledger.WriteAll(os.Stdout, txs, entries)
		}
		return writeLedger(s, txs, entries)
	default:
		return fmt.Errorf("unknown format %q: want table, json, or ledger", *format)
	}
}

// filterByAccount keeps the lines with a posting whose account matches the pattern,
// case-insensitively, anywhere in the path. Ledger matches on the whole account name the same way,
// which is what lets one pattern find Uncategorized at every depth: bare, or as a truncated leaf.
func filterByAccount(pattern string, txs []model.Transaction, entries []model.Entry) ([]model.Transaction, []model.Entry, error) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, nil, fmt.Errorf("-account %q is not a valid pattern: %v", pattern, err)
	}

	var keptTxs []model.Transaction
	var keptEntries []model.Entry
	for i, tx := range txs {
		for _, p := range entries[i].Postings {
			if re.MatchString(p.Account) {
				keptTxs = append(keptTxs, tx)
				keptEntries = append(keptEntries, entries[i])
				break
			}
		}
	}
	return keptTxs, keptEntries, nil
}

// report renders the books for a person: the fingerprint first, because it is the handle every
// correction takes, then the line and where it posted.
func report(out io.Writer, txs []model.Transaction, entries []model.Entry) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FINGERPRINT\tDATE\tPAYEE\tAMOUNT\tPOSTS TO")

	var unknown int
	for i, tx := range txs {
		e := entries[i]
		if e.Uncategorized() {
			unknown++
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			tx.ID, tx.Date.Format("2006-01-02"), e.Payee, tx.Amount, accounts(e))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	// Every line posts, so the only thing left to say is where the rules ran out, and how to see
	// only those lines.
	if unknown > 0 {
		fmt.Fprintf(out, "\n%d lines posted, %d of them uncategorized (bk books -account Uncategorized shows only them)\n", len(txs), unknown)
	} else {
		fmt.Fprintf(out, "\n%d lines posted, %d of them uncategorized\n", len(txs), unknown)
	}
	return nil
}

// bookLine is one line of the books, machine-readable: the fingerprint a correction is keyed by,
// and enough of the line to decide anything about it. This is the surface an external model reads
// (books -account Uncategorized -format json is the decision queue); it answers back through
// categorize and rules set.
type bookLine struct {
	Fingerprint string   `json:"fingerprint"`
	Date        string   `json:"date"`
	Account     string   `json:"account"`
	Amount      string   `json:"amount"`
	Description string   `json:"description"`
	Payee       string   `json:"payee"`
	PostsTo     []string `json:"posts_to"`
}

// bookLines flattens the fold into the machine-readable shape, one item per line.
func bookLines(txs []model.Transaction, entries []model.Entry) []bookLine {
	lines := make([]bookLine, len(txs))
	for i, tx := range txs {
		e := entries[i]
		postsTo := make([]string, len(e.Postings))
		for j, p := range e.Postings {
			postsTo[j] = p.Account
		}
		lines[i] = bookLine{
			Fingerprint: tx.ID,
			Date:        tx.Date.Format("2006-01-02"),
			Account:     tx.Account,
			Amount:      tx.Amount.String(),
			Description: tx.Description,
			Payee:       e.Payee,
			PostsTo:     postsTo,
		}
	}
	return lines
}

func writeJSON(w io.Writer, txs []model.Transaction, entries []model.Entry) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Lines []bookLine `json:"lines"`
	}{bookLines(txs, entries)})
}

// writeLedger regenerates the artifact in place. The books are a fold, so the ledger is derived
// output: bookkeeper owns the file and rewrites it whole, and its git diff is the readable account
// of what changed.
func writeLedger(s *store.Store, txs []model.Transaction, entries []model.Entry) error {
	f, err := os.Create(s.Ledger())
	if err != nil {
		return err
	}
	if err := ledger.WriteAll(f, txs, entries); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("wrote %d entries to %s\n", len(txs), s.Ledger())
	return nil
}

// accounts names where an entry posted. A split posted to more than one place, and hiding that
// would misreport the books.
func accounts(e model.Entry) string {
	names := make([]string, len(e.Postings))
	for i, p := range e.Postings {
		names[i] = p.Account
	}
	return strings.Join(names, " + ")
}
