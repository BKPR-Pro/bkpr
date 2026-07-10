// Command bookkeeper turns bank and card statements into a set of books.
//
// `import` records what a statement said, once per line, into an append-only log. `books` folds
// that log back out through a rule set and renders it. Where the rules run out of knowledge the
// account path stops at Uncategorized rather than guessing, and later slices hand those to a model
// and then to a person.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/dallasread/bookkeeper/cli/internal/books"
	"github.com/dallasread/bookkeeper/cli/internal/eventlog"
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

	var err error
	switch os.Args[1] {
	case "import":
		err = importStatement(os.Args[2:])
	case "books":
		err = renderBooks(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "bookkeeper: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `bookkeeper - turn statements into books

usage:
  bookkeeper import -db <file> -mapping <file> -csv <file>
  bookkeeper books  -db <file> -rules <file> [-format table|ledger]
`)
}

// openLog is the only place that decides where the books live.
func openLog(path string) (*eventlog.Log, func() error, error) {
	store, err := eventlog.OpenSQLite(path)
	if err != nil {
		return nil, nil, err
	}
	return eventlog.New(store), store.Close, nil
}

func importStatement(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	dbPath := fs.String("db", "books.db", "the event log")
	mappingPath := fs.String("mapping", "", "JSON describing how this bank's CSV columns map onto a transaction")
	csvPath := fs.String("csv", "", "CSV statement to read")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *mappingPath == "" || *csvPath == "" {
		fs.Usage()
		return fmt.Errorf("mapping and csv are both required")
	}

	mapping, err := loadMapping(*mappingPath)
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

	log, closeLog, err := openLog(*dbPath)
	if err != nil {
		return err
	}
	defer closeLog()

	// The actor records which statement reported a line, so a bad mapping is traceable to the file
	// that carried it.
	result, err := books.Import(log, "statement:"+filepath.Base(*csvPath), txs)
	if err != nil {
		return err
	}

	fmt.Printf("%d lines read: %d imported, %d already in the log\n", len(txs), result.Imported, result.Skipped)
	return nil
}

func renderBooks(args []string) error {
	fs := flag.NewFlagSet("books", flag.ExitOnError)
	dbPath := fs.String("db", "books.db", "the event log")
	rulesPath := fs.String("rules", "", "JSON rule set")
	format := fs.String("format", "table", "output format: table or ledger")
	currency := fs.String("currency", "CAD", "currency written on ledger postings")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *rulesPath == "" {
		fs.Usage()
		return fmt.Errorf("rules is required")
	}

	engine, err := rules.Load(*rulesPath)
	if err != nil {
		return err
	}

	log, closeLog, err := openLog(*dbPath)
	if err != nil {
		return err
	}
	defer closeLog()

	txs, err := books.Transactions(log)
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
