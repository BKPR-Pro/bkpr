// Command bookkeeper turns bank and card statements into a set of books.
//
// A directory holds a set of books the way it holds a git repository, marked by `.bookkeeper` and
// found by walking up. `import` records what a statement said, once per line, into the append-only
// log inside it. `books` folds that log back out through a rule set and renders it. Where the
// rules run out of knowledge the account path stops at Uncategorized rather than guessing, and
// later slices hand those to a model and then to a person.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/dallasread/bookkeeper/lib/adapters/ledger"
	"github.com/dallasread/bookkeeper/lib/adapters/source"
	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/rules"
	"github.com/dallasread/bookkeeper/lib/store"
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
	case "docs":
		docs(os.Stdout)
	case "help", "-h", "--help":
		usage()
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
Run "bookkeeper docs" for the full reference.

usage:
  bookkeeper init         [dir]
  bookkeeper rules   add  -match <re> -category <account> [-payee <name>] [-meta <k=v> ...] [-before <re>]
  bookkeeper rules   set  -match <re> [-category <account>] [-payee <name>] [-why <reason>]
  bookkeeper rules   rm   -match <re>
  bookkeeper rules   mv   -match <re> [-before <re>]
  bookkeeper rules   list
  bookkeeper sources add  <name> -kind rentapp -url <url> -token-env <ENV> -account <a> [-currency <c>]
  bookkeeper sources rm   <name>
  bookkeeper sources list
  bookkeeper import       <file.csv> -account <a> -currency <c> (-amount <col> | -debit <col> -credit <col>) [-date <col> -description <col> -date-format <layout>]
  bookkeeper import       <file.ledger>
  bookkeeper categorize   -tx <fingerprint> (-category <account> | -post <account>=<amount> ...) [-payee <name>] [-why <reason>]
  bookkeeper discard      -tx <fingerprint> [-why <reason>]
  bookkeeper books        [-format table|ledger] [-stdout]
  bookkeeper docs
`)
}

// docs prints the full command reference, so the CLI is self-documenting.
func docs(w io.Writer) {
	fmt.Fprint(w, `bookkeeper - turn bank and card statements into a plain-text double-entry ledger.

A set of books lives in a .bookkeeper directory, found by walking up from the current
directory the way git finds .git. The log inside it (log.jsonl) is the book of record;
everything else, including the ledger artifact, is a fold over it and is regenerated.

SETUP
  init [dir]
      Create a set of books in dir (default: here).

  sources add <name> -kind <kind> -url <url> -token-env <ENV> -account <a> [-currency <c>]
      Register a live connector. The bearer token is never stored: -token-env names the
      environment variable that holds it, read when the connector is used. This is the
      registry the push command draws on; bookkeeper does not import from a connector,
      since the bank statement is the source of truth for money.
  sources rm <name>               Forget a connector.
  sources list                    Show the registered connectors.

RULES  (deterministic categorization; first matching rule wins per field)
  rules add -match <re> -category <account> [-payee <name>] [-meta <k=v> ...] [-before <re>]
      Add a rule. Order decides which of two matching rules wins; a new rule lands last
      unless -before places it ahead of another. Account paths are free-form and may stop
      at Uncategorized wherever knowledge runs out. -meta attaches opaque key=value pairs
      (repeatable) that a destination reads by name, e.g. -meta rentapp.lease=31 tells the
      push which lease a matching rent deposit belongs to.
  rules set -match <re> [-category <account>] [-payee <name>] [-why <reason>]
      Change an existing rule; only the fields you name change. This reclassifies every
      past line the rule matched, so it takes a reason.
  rules rm  -match <re>           Remove a rule.
  rules mv  -match <re> [-before <re>]   Reorder a rule (-before omitted moves it last).
  rules list                      Show the rules in order.

BOOKKEEPING
  import <file.csv> -account <a> -currency <c> (-amount <col> | -debit <col> -credit <col>)
                    [-date <col>] [-description <col>] [-date-format <layout>]
  import <file.ledger>
      Import transactions from a statement file. A file is a one-time input: a CSV does not
      name its own account, currency, or columns, so you supply them inline; a ledger file
      names all of that itself. Bookkeeper imports only from files; the bank statement is
      the source of truth for money, so nothing is ever pulled from a live app.
  categorize -tx <fingerprint> (-category <account> | -post <account>=<amount> ...)
             [-payee <name>] [-why <reason>]
      Assert the postings for one line, overriding the rule for that line only. Use -post
      more than once to split one charge across accounts.
  discard -tx <fingerprint> [-why <reason>]
      Drop a bad import from the books. The imported fact stays in the log; a later fact
      supersedes it.
  books [-format table|ledger] [-stdout]
      Fold the log into a table (default), or regenerate .bookkeeper/books.ledger. -stdout
      writes the ledger to standard output instead of the store.

Fingerprints come from the log; find an uncategorized line's fingerprint there to
categorize it. See the README for the design.
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

// open finds the book of record and hands back its log with a closer.
func open() (*eventlog.Log, func() error, error) {
	s, err := store.Open(".")
	if err != nil {
		return nil, nil, err
	}
	return s.Log, s.Close, nil
}

// firstArg peels a required positional argument off the front, before any flags.
func firstArg(args []string, desc string) (string, []string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", nil, fmt.Errorf("expected %s as the first argument", desc)
	}
	return args[0], args[1:], nil
}

// importStatement reads a statement file into the log. A file is a one-time input whose details
// are supplied inline (a CSV does not name its own account, currency, or columns). The bank
// statement is the source of truth for money; bookkeeper never imports from a live app.
func importStatement(args []string) error {
	arg, rest, err := firstArg(args, "a file to import")
	if err != nil {
		return err
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	switch ext := strings.ToLower(filepath.Ext(arg)); ext {
	case ".csv":
		return importCSV(s.Log, arg, rest)
	default:
		return fmt.Errorf("import: I do not know how to read %q; .csv is supported, ledger files are coming", arg)
	}
}

func importCSV(log *eventlog.Log, path string, args []string) error {
	fs := flag.NewFlagSet("import (csv)", flag.ExitOnError)
	var m source.CSV
	fs.StringVar(&m.Account, "account", "", "the ledger account this statement belongs to")
	fs.StringVar(&m.Currency, "currency", "", "the account's currency, e.g. CAD")
	fs.StringVar(&m.Date, "date", "Date", "header of the date column")
	fs.StringVar(&m.Description, "description", "Description", "header of the memo column")
	fs.StringVar(&m.DateFormat, "date-format", "2006-01-02", "Go date layout the column uses")
	fs.StringVar(&m.Amount, "amount", "", "header of a single signed amount column")
	fs.StringVar(&m.Debit, "debit", "", "header of the debit column, if amounts are a pair")
	fs.StringVar(&m.Credit, "credit", "", "header of the credit column, if amounts are a pair")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case m.Account == "":
		return fmt.Errorf("-account is required: which account's statement is this?")
	case m.Currency == "":
		return fmt.Errorf("-currency is required")
	case m.Amount == "" && m.Debit == "" && m.Credit == "":
		return fmt.Errorf("-amount, or -debit and -credit, is required")
	}

	statement, err := os.Open(path)
	if err != nil {
		return err
	}
	defer statement.Close()

	txs, err := source.ReadCSV(statement, m)
	if err != nil {
		return err
	}

	result, err := books.Import(log, "statement:"+filepath.Base(path), txs)
	if err != nil {
		return err
	}

	fmt.Printf("%d lines read: %d imported, %d already in the log\n", len(txs), result.Imported, result.Skipped)
	return nil
}

// sourceSet dispatches `sources add|rm|list`.
func sourceSet(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("sources needs add, rm, or list")
	}
	switch args[0] {
	case "add":
		return sourceAdd(args[1:])
	case "rm":
		return sourceRemove(args[1:])
	case "list":
		return sourceList(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown sources subcommand %q", args[0])
	}
}

func sourceAdd(args []string) error {
	name, rest, err := firstArg(args, "a name for the source, e.g. rent")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("sources add", flag.ExitOnError)
	var s books.Source
	fs.StringVar(&s.Kind, "kind", "rentapp", "which connector this source uses")
	fs.StringVar(&s.URL, "url", "", "the connector's base URL")
	fs.StringVar(&s.TokenEnv, "token-env", "", "the environment variable holding its bearer token")
	fs.StringVar(&s.Account, "account", "", "the ledger account its transactions land in")
	fs.StringVar(&s.Currency, "currency", "CAD", "the currency of its transactions")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	s.Name = name

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.AddSource(log, "human", s); err != nil {
		return err
	}
	fmt.Printf("source %s\n", s.Name)
	return nil
}

func sourceRemove(args []string) error {
	name, _, err := firstArg(args, "the source to forget")
	if err != nil {
		return err
	}
	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.RemoveSource(log, "human", name); err != nil {
		return err
	}
	fmt.Printf("removed source %s\n", name)
	return nil
}

func sourceList(args []string) error {
	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	set, err := books.Sources(log)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tURL\tACCOUNT\tCURRENCY\tTOKEN-ENV")
	for _, s := range set {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Kind, s.URL, s.Account, s.Currency, s.TokenEnv)
	}
	return w.Flush()
}

// ruleSet dispatches `rules add|set|rm|mv|list`.
func ruleSet(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("rules needs add, set, rm, mv, or list")
	}
	switch args[0] {
	case "add":
		return ruleAdd(args[1:])
	case "set":
		return ruleSetOne(args[1:])
	case "rm":
		return ruleRemove(args[1:])
	case "mv":
		return ruleMove(args[1:])
	case "list":
		return ruleList(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown rules subcommand %q", args[0])
	}
}

// metaFlag collects repeated -meta key=value pairs into a rule's opaque metadata bag. The key is a
// namespaced identifier a destination reads (e.g. rentapp.lease); the value is kept verbatim.
type metaFlag map[string]string

func (m metaFlag) String() string { return "" }

func (m *metaFlag) Set(s string) error {
	i := strings.Index(s, "=")
	if i < 0 {
		return fmt.Errorf("metadata %q must be key=value", s)
	}
	key := strings.TrimSpace(s[:i])
	if key == "" {
		return fmt.Errorf("metadata %q has an empty key", s)
	}
	if *m == nil {
		*m = metaFlag{}
	}
	(*m)[key] = s[i+1:]
	return nil
}

func ruleAdd(args []string) error {
	fs := flag.NewFlagSet("rules add", flag.ExitOnError)
	var r rules.Rule
	var meta metaFlag
	fs.StringVar(&r.Match, "match", "", "case-insensitive pattern to match the description")
	fs.StringVar(&r.Category, "category", "", "account to post the line to")
	fs.StringVar(&r.Payee, "payee", "", "payee to record on the entry")
	fs.Var(&meta, "meta", "key=value carried onto the entry, repeatable, e.g. rentapp.lease=31")
	before := fs.String("before", "", "place this rule ahead of the one matching this pattern")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if r.Match == "" {
		return fmt.Errorf("-match is required")
	}
	if len(meta) > 0 {
		r.Metadata = meta
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.AddRule(log, "human", r, *before); err != nil {
		return err
	}
	fmt.Printf("rule %q\n", r.Match)
	return nil
}

func ruleSetOne(args []string) error {
	fs := flag.NewFlagSet("rules set", flag.ExitOnError)
	match := fs.String("match", "", "the rule to change")
	category := fs.String("category", "", "the new account to post to")
	payee := fs.String("payee", "", "the new payee")
	why := fs.String("why", "", "why the rule changed; it reclassifies every line it matched")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *match == "" {
		return fmt.Errorf("-match is required")
	}

	// Only the fields you name change; the rest of the rule is left as it was, so setting a category
	// does not silently blank the payee.
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	current, err := books.Rules(log)
	if err != nil {
		return err
	}
	merged, found := rules.Rule{}, false
	for _, r := range current {
		if r.Match == *match {
			merged, found = r, true
			break
		}
	}
	if !found {
		return fmt.Errorf("no rule matches %q; add it first", *match)
	}
	if provided["category"] {
		merged.Category = *category
	}
	if provided["payee"] {
		merged.Payee = *payee
	}

	if err := books.SetRule(log, "human", *why, merged); err != nil {
		return err
	}
	fmt.Printf("rule %q\n", *match)
	return nil
}

func ruleRemove(args []string) error {
	fs := flag.NewFlagSet("rules rm", flag.ExitOnError)
	match := fs.String("match", "", "the rule to remove")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *match == "" {
		return fmt.Errorf("-match is required")
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.RemoveRule(log, "human", *match); err != nil {
		return err
	}
	fmt.Printf("removed rule %q\n", *match)
	return nil
}

func ruleMove(args []string) error {
	fs := flag.NewFlagSet("rules mv", flag.ExitOnError)
	match := fs.String("match", "", "the rule to move")
	before := fs.String("before", "", "move it ahead of this rule; omit to move it to the end")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *match == "" {
		return fmt.Errorf("-match is required")
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.MoveRule(log, "human", *match, *before); err != nil {
		return err
	}
	fmt.Printf("moved rule %q\n", *match)
	return nil
}

func ruleList(args []string) error {
	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

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
