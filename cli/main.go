// Command bookkeeper categorizes bank and card statements into a set of books.
//
// This slice covers the deterministic tier: read a CSV statement, apply an ordered rule set, and
// report what each line categorizes to. Lines no rule can categorize are surfaced rather than
// guessed at; later slices escalate those to a model and then to a human, and record the answer
// as a new rule.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/dallasread/bookkeeper/cli/internal/ledger"
	"github.com/dallasread/bookkeeper/cli/internal/model"
	"github.com/dallasread/bookkeeper/cli/internal/rules"
	"github.com/dallasread/bookkeeper/cli/internal/source"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "categorize":
		if err := categorize(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "bookkeeper: %v\n", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `bookkeeper - categorize statements into books

usage:
  bookkeeper categorize -mapping <file> -rules <file> -csv <file>
`)
}

func categorize(args []string) error {
	fs := flag.NewFlagSet("categorize", flag.ExitOnError)
	mappingPath := fs.String("mapping", "", "JSON describing how this bank's CSV columns map onto a transaction")
	rulesPath := fs.String("rules", "", "JSON rule set")
	csvPath := fs.String("csv", "", "CSV statement to read")
	format := fs.String("format", "table", "output format: table or ledger")
	currency := fs.String("currency", "CAD", "currency written on ledger postings")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *mappingPath == "" || *rulesPath == "" || *csvPath == "" {
		fs.Usage()
		return fmt.Errorf("mapping, rules, and csv are all required")
	}

	mapping, err := loadMapping(*mappingPath)
	if err != nil {
		return err
	}
	engine, err := rules.Load(*rulesPath)
	if err != nil {
		return err
	}
	statement, err := os.Open(*csvPath)
	if err != nil {
		return err
	}
	defer statement.Close()

	txs, err := source.ReadCSV(statement, mapping)
	if err != nil {
		return err
	}

	decisions := make([]model.Decision, len(txs))
	for i, tx := range txs {
		decisions[i] = engine.Apply(tx)
	}

	switch *format {
	case "table":
		return report(os.Stdout, txs, decisions)
	case "ledger":
		return ledger.WriteAll(os.Stdout, txs, decisions, *currency)
	default:
		return fmt.Errorf("unknown format %q: want table or ledger", *format)
	}
}

func loadMapping(path string) (source.Mapping, error) {
	var m source.Mapping
	body, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return m, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

func report(out *os.File, txs []model.Transaction, decisions []model.Decision) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DATE\tPAYEE\tAMOUNT\tCATEGORY")

	var reviewed int
	for i, tx := range txs {
		d := decisions[i]

		payee := d.Payee
		if payee == "" {
			payee = tx.Description
		}

		category := d.Category
		if d.NeedsReview {
			reviewed++
			category = "NEEDS REVIEW (" + d.Reason + ")"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", tx.Date.Format("2006-01-02"), payee, dollars(tx.AmountCents), category)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(out, "\n%d lines: %d categorized, %d need review\n", len(txs), len(txs)-reviewed, reviewed)
	return nil
}

func dollars(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}
