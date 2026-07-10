// Command bookkeeper turns bank and card statements into a set of books.
//
// A directory holds a set of books the way it holds a git repository, marked by `.bookkeeper` and
// found by walking up. `import` records what a statement said, once per line, into the append-only
// log inside it. `books` folds that log back out through a rule set and renders it. Where the
// rules run out of knowledge the account path stops at Uncategorized rather than guessing, and
// later slices hand those to a model and then to a person.
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
	"github.com/dallasread/bookkeeper/cli/internal/ledger"
	"github.com/dallasread/bookkeeper/cli/internal/model"
	"github.com/dallasread/bookkeeper/cli/internal/rules"
	"github.com/dallasread/bookkeeper/cli/internal/source"
	"github.com/dallasread/bookkeeper/cli/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "init":
		err = initStore(os.Args[2:])
	case "import":
		err = importStatement(os.Args[2:])
	case "sources":
		err = sourceSet(os.Args[2:])
	case "rules":
		err = ruleSet(os.Args[2:])
	case "categorize":
		err = categorize(os.Args[2:])
	case "discard":
		err = discard(os.Args[2:])
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

Every command finds the nearest .bookkeeper directory by walking up, as git does.

usage:
  bookkeeper init         [dir]
  bookkeeper sources load [-file <file>] [-why <reason>]
  bookkeeper sources list
  bookkeeper rules   load [-file <file>] [-why <reason>]
  bookkeeper rules   list
  bookkeeper import       -source <account> -csv <file>
  bookkeeper categorize   -tx <fingerprint> (-category <account> | -post <account>=<amount> ...) [-payee <name>] [-why <reason>]
  bookkeeper discard      -tx <fingerprint> [-why <reason>]
  bookkeeper books        [-format table|ledger] [-stdout]
`)
}

func initStore(args []string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	path, err := store.Init(dir)
	if err != nil {
		return err
	}
	fmt.Printf("Initialized a book of record in %s\n", path)
	return nil
}

func importStatement(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	account := fs.String("source", "", "the ledger account this statement belongs to")
	csvPath := fs.String("csv", "", "CSV statement to read")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *account == "" || *csvPath == "" {
		fs.Usage()
		return fmt.Errorf("source and csv are both required")
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()
	log := s.Log

	src, err := books.Source(log, *account)
	if err != nil {
		return err
	}

	statement, err := os.Open(*csvPath)
	if err != nil {
		return err
	}
	defer statement.Close()

	txs, err := source.ReadCSV(statement, src)
	if err != nil {
		return err
	}

	// The actor records which statement reported a line, so a bad source is traceable to the file
	// that carried it.
	result, err := books.Import(log, "statement:"+filepath.Base(*csvPath), txs)
	if err != nil {
		return err
	}

	fmt.Printf("%d lines read: %d imported, %d already in the log\n", len(txs), result.Imported, result.Skipped)
	return nil
}

// sourceSet records a sources file into the log, or shows what the log currently says.
func sourceSet(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("sources needs load or list")
	}

	fs := flag.NewFlagSet("sources "+args[0], flag.ExitOnError)
	filePath := fs.String("file", "", "JSON source set to load")
	why := fs.String("why", "", "why the sources changed; recorded with the edit")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()
	log := s.Log

	switch args[0] {
	case "list":
		set, err := books.Sources(log)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ACCOUNT\tCURRENCY\tDATE\tDESCRIPTION\tAMOUNT")
		for _, s := range set {
			amount := s.Amount
			if amount == "" {
				amount = s.Debit + " / " + s.Credit
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Account, s.Currency, s.Date, s.Description, amount)
		}
		return w.Flush()

	case "load":
		if *filePath == "" {
			fs.Usage()
			return fmt.Errorf("file is required")
		}
		var want []source.CSV
		body, err := os.ReadFile(*filePath)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(body, &want); err != nil {
			return fmt.Errorf("%s: %w", *filePath, err)
		}
		got, err := books.LoadSources(log, "human", *why, want)
		if err != nil {
			return err
		}
		fmt.Printf("%d sources: %d added, %d changed, %d removed\n",
			len(want), got.Added, got.Changed, got.Removed)
		return nil

	default:
		usage()
		return fmt.Errorf("unknown sources subcommand %q", args[0])
	}
}

// ruleSet records a rules file into the log, or shows what the log currently says.
func ruleSet(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("rules needs load or list")
	}

	fs := flag.NewFlagSet("rules "+args[0], flag.ExitOnError)
	filePath := fs.String("file", "", "JSON rule set to load")
	why := fs.String("why", "", "why the rule set changed; recorded with the edit")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()
	log := s.Log

	switch args[0] {
	case "list":
		set, err := books.Rules(log)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "MATCH\tPAYEE\tCATEGORY")
		for _, r := range set {
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Match, r.Payee, r.Category)
		}
		return w.Flush()

	case "load":
		if *filePath == "" {
			fs.Usage()
			return fmt.Errorf("file is required")
		}
		want, err := rules.ReadFile(*filePath)
		if err != nil {
			return err
		}
		got, err := books.LoadRules(log, "human", *why, want)
		if err != nil {
			return err
		}
		fmt.Printf("%d rules: %d added, %d changed, %d removed, %d moved\n",
			len(want), got.Added, got.Changed, got.Removed, got.Moved)
		return nil

	default:
		usage()
		return fmt.Errorf("unknown rules subcommand %q", args[0])
	}
}

// rawPosting is an account and an unparsed quantity from a -post flag. The quantity becomes an
// Amount only once the transaction's commodity is known, which is after the store is open.
type rawPosting struct{ account, quantity string }

// splitFlag collects repeated -post account=amount flags, so a correction can be a split.
type splitFlag []rawPosting

func (p *splitFlag) String() string { return "" }

func (p *splitFlag) Set(s string) error {
	i := strings.LastIndex(s, "=")
	if i < 0 {
		return fmt.Errorf("posting %q must be account=amount", s)
	}
	*p = append(*p, rawPosting{account: strings.TrimSpace(s[:i]), quantity: strings.TrimSpace(s[i+1:])})
	return nil
}

// categorize records a human's answer for one line: its category, or a split across several.
func categorize(args []string) error {
	var split splitFlag
	fs := flag.NewFlagSet("categorize", flag.ExitOnError)
	txID := fs.String("tx", "", "the transaction fingerprint to categorize")
	category := fs.String("category", "", "post the whole line to this one account")
	payee := fs.String("payee", "", "the payee to record on the entry")
	why := fs.String("why", "", "why this line is categorized so; recorded with the assertion")
	fs.Var(&split, "post", "account=amount, repeatable, for a line that splits across accounts")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *txID == "" || (*category == "" && len(split) == 0) {
		fs.Usage()
		return fmt.Errorf("tx and one of -category or -post are required")
	}
	if *category != "" && len(split) > 0 {
		return fmt.Errorf("give -category or -post, not both")
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	// The line's own commodity is what a posting is denominated in, so it is fetched before the
	// postings are built and the caller never restates it.
	tx, err := books.Transaction(s.Log, *txID)
	if err != nil {
		return err
	}

	var post []model.Posting
	if *category != "" {
		// The common case: the whole line to one account, in the line's amount with the opposite sign.
		post = []model.Posting{{Account: *category, Amount: tx.Amount.Negate()}}
	} else {
		for _, rp := range split {
			amount, err := model.NewAmount(rp.quantity, tx.Amount.Commodity)
			if err != nil {
				return err
			}
			post = append(post, model.Posting{Account: rp.account, Amount: amount})
		}
	}

	if err := books.Categorize(s.Log, "human", *why, *txID, *payee, post); err != nil {
		return err
	}
	fmt.Printf("categorized %s\n", *txID)
	return nil
}

// discard removes a garbage line from the books, the way to undo a bad import.
func discard(args []string) error {
	fs := flag.NewFlagSet("discard", flag.ExitOnError)
	txID := fs.String("tx", "", "the transaction fingerprint to discard")
	why := fs.String("why", "", "why the line is garbage; recorded with the discard")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *txID == "" {
		fs.Usage()
		return fmt.Errorf("tx is required")
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	if err := books.Discard(s.Log, "human", *why, *txID); err != nil {
		return err
	}
	fmt.Printf("discarded %s\n", *txID)
	return nil
}

func renderBooks(args []string) error {
	fs := flag.NewFlagSet("books", flag.ExitOnError)
	format := fs.String("format", "table", "output format: table or ledger")
	stdout := fs.Bool("stdout", false, "write the ledger to stdout instead of the store")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	txs, entries, err := books.Ledger(s.Log)
	if err != nil {
		return err
	}

	switch *format {
	case "table":
		return report(os.Stdout, txs, entries)
	case "ledger":
		if *stdout {
			return ledger.WriteAll(os.Stdout, txs, entries)
		}
		return writeLedger(s, txs, entries)
	default:
		return fmt.Errorf("unknown format %q: want table or ledger", *format)
	}
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
			tx.Date.Format("2006-01-02"), e.Payee, tx.Amount, accounts(e))
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
