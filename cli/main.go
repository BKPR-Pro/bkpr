// Command bookkeeper categorizes bank and card statements into a set of books.
//
// This slice covers the deterministic tier: read a CSV statement, apply an ordered rule set, and
// report where each line posts. Every line posts. Where the rules run out of knowledge the account
// path stops at Uncategorized rather than guessing, and later slices hand those to a model and
// then to a person.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
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

	entries := make([]model.Entry, len(txs))
	for i, tx := range txs {
		entries[i] = engine.Apply(tx)
	}

	switch *format {
	case "table":
		return report(os.Stdout, txs, entries)
	case "ledger":
		return ledger.WriteAll(os.Stdout, txs, entries, *currency)
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

func report(out *os.File, txs []model.Transaction, entries []model.Entry) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DATE\tPAYEE\tAMOUNT\tPOSTS TO")

	var unknown int
	for i, tx := range txs {
		e := entries[i]
		if e.Uncategorized() {
			unknown++
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			tx.Date.Format("2006-01-02"), e.Payee, dollars(tx.AmountCents), accounts(e))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	// Every line posts, so the only thing left to say is where the rules ran out.
	fmt.Fprintf(out, "\n%d lines posted, %d of them uncategorized\n", len(txs), unknown)
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

func dollars(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}
