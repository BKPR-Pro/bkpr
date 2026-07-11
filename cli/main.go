// Command bookkeeper turns bank and card statements into a set of books.
//
// A directory holds a set of books the way it holds a git repository, marked by `.bookkeeper` and
// found by walking up. `import` records what a statement said, once per line, into the append-only
// log inside it. `books` folds that log back out through a rule set and renders it. Where the
// rules run out of knowledge the account path stops at Uncategorized rather than guessing, and the
// agent operating the tool answers those through the same commands a person would. bookkeeper is
// built to be driven by an agent; it holds no model of its own.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

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
		err = importCmd(os.Args[2:])
	case "connectors":
		err = connectorSet(os.Args[2:])
	case "rules":
		err = ruleSet(os.Args[2:])
	case "categorize":
		err = categorize(os.Args[2:])
	case "void":
		err = voidCmd(os.Args[2:])
	case "match":
		err = match(os.Args[2:])
	case "invoice":
		err = invoiceCmd(os.Args[2:])
	case "bill":
		err = billCmd(os.Args[2:])
	case "review":
		err = review(os.Args[2:])
	case "policy":
		err = policyCmd(os.Args[2:])
	case "accounts":
		err = accountCmd(os.Args[2:])
	case "receipt":
		err = receiptCmd(os.Args[2:])
	case "report":
		err = reportCmd(os.Args[2:])
	case "export":
		err = exportCmd(os.Args[2:])
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
  bookkeeper rules   set  -match <re> [-category <account>] [-payee <name>] [-meta <k=v> ...] [-before <re>] [-why <reason>] [-actor <name>]
  bookkeeper rules   rm   -match <re>
  bookkeeper rules   mv   -match <re> [-before <re>]
  bookkeeper rules   list
  bookkeeper connectors register <name> -kind rentapp -url <url> -token-env <ENV> -account <a> [-currency <c>]
  bookkeeper connectors rm   <name>
  bookkeeper connectors list
  bookkeeper import       <file.csv> -account <a> -currency <c> (-amount <col> | -debit <col> -credit <col>) [-date <col> -description <col> -date-format <layout>]
  bookkeeper import       <file.ledger>
  bookkeeper import       <connector>
  bookkeeper categorize   -tx <fingerprint> (-category <account> | -post <account>=<amount> ...) [-payee <name>] [-why <reason>] [-actor <name>]
  bookkeeper void         -tx <fingerprint> [-why <reason>] [-actor <name>]
  bookkeeper match        -tx <fingerprint> (-with <fingerprint> | -break) [-actor <name>]
  bookkeeper policy  set  -method <acb|fifo> [-account <a>] [-actor <name>]
  bookkeeper policy  list
  bookkeeper accounts set  <account> -meta <k=v> ... [-actor <name>]
  bookkeeper accounts list
  bookkeeper receipt  -tx <fingerprint> [-as invoice|receipt] [-format text|html] [-out <file>]
  bookkeeper report   [-income | -balance | -gains] [-format text|html] [-account <text>] [-from <D>] [-to <D>] [-out <file>]
  bookkeeper invoice raise   -party <name> -amount <amt> -category <account> [-account <a>] [-date <YYYY-MM-DD>] [-currency <c>] [-why <reason>] [-actor <name>]
  bookkeeper invoice settle  -id <fingerprint> (-tx <fingerprint> | -reopen) [-actor <name>]
  bookkeeper invoice void    -id <fingerprint> [-why <reason>] [-actor <name>]
  bookkeeper invoice list
  bookkeeper invoice aging   [-as-of <YYYY-MM-DD>]
  bookkeeper bill    receive -party <name> -amount <amt> -category <account> [-account <a>] [-date <YYYY-MM-DD>] [-currency <c>] [-why <reason>] [-actor <name>]
  bookkeeper bill    settle  -id <fingerprint> (-tx <fingerprint> | -reopen) [-actor <name>]
  bookkeeper bill    void    -id <fingerprint> [-why <reason>] [-actor <name>]
  bookkeeper bill    list
  bookkeeper bill    aging   [-as-of <YYYY-MM-DD>]
  bookkeeper review
  bookkeeper export       <connector> [-confirm]
  bookkeeper books        [-format table|ledger] [-basis cash|accrual] [-stdout]
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

  connectors register <name> -kind <kind> -url <url> -token-env <ENV> -account <a> [-currency <c>]
      Register a live connector. The bearer token is never stored: -token-env names the
      environment variable that holds it, read when the connector is used. A connector is
      bidirectional in principle: export writes to it today, and importing from it by name
      is the same registry, built later. Registering one does not itself move any data.
  connectors rm <name>            Forget a connector.
  connectors list                 Show the registered connectors.

RULES  (deterministic categorization; first matching rule wins per field)
  rules set -match <re> [-category <account>] [-payee <name>] [-meta <k=v> ...] [-before <re>] [-why <reason>]
      Add a rule, or change one already matching this pattern. On an existing rule only the
      fields you name change, and since that reclassifies every past line it matched, it
      takes a -why. A new rule lands last unless -before places it ahead of another. Account
      paths are free-form and may stop at Uncategorized wherever knowledge runs out. -meta
      attaches opaque key=value pairs (repeatable) that a connector reads by name, e.g.
      -meta rentapp.lease=31 tells the export which lease a matching rent deposit belongs to.
  rules rm  -match <re>           Remove a rule.
  rules mv  -match <re> [-before <re>]   Reorder a rule (-before omitted moves it last).
  rules list                      Show the rules in order.

BOOKKEEPING
  import <file.csv> -account <a> -currency <c> (-amount <col> | -debit <col> -credit <col>)
                    [-date <col>] [-description <col>] [-date-format <layout>]
  import <file.ledger>
  import <connector>
      Read transactions in. A file is a one-time input: a CSV does not name its own account,
      currency, or columns, so you supply them inline; a ledger file names all of that itself
      (each entry's single amountless posting is the account it came from). The line is
      imported raw and the rules place it, so a ledger file's own categorization is not
      carried in. A connector is named instead of a file: it already carries its account and
      currency (from connectors register) and fetches its own lines. All three land through the
      same import, deduped by fingerprint, so re-running is safe. A bank connector (kind rbc,
      simplii, or pcfinancial) drives a headless-browser Playwright script to read the account,
      for the Canadian banks with no free API; the scripts are stubs today, so until one is
      written its import fails loudly and a manual CSV export is the way in. rentapp is
      export-only.
  categorize -tx <fingerprint> (-category <account> | -post <account>=<amount> ... |
             -sell <account>=<qty> ... -gain <account>) [-payee <name>] [-why <reason>] [-actor <name>]
      Assert the postings for one line, overriding the rule for that line only. Use -post
      more than once to split one charge across accounts. A -post amount may name its own
      commodity and an @@ total price, so a share bought with cash is
      -post "Assets:Brokerage:AAPL=10 AAPL @@ 1000.00 USD". A sale instead names the shares
      it disposed of with -sell and where the gain lands with -gain; the cost base, and so the
      gain, is folded from your purchases: -sell "Assets:Brokerage:AAPL=10 AAPL" -gain "Income:Capital Gains".
      -actor records who decided (default human, e.g. -actor claude when an agent runs it), so
      the log tells hands apart without the tool caring whose; rules set and void take it too.
  void -tx <fingerprint> [-why <reason>] [-actor <name>]
      Annul a bad imported line. The imported fact stays in the log; a later fact supersedes
      it. Voiding an invoice is the same verb on a different noun: invoice void.
  match -tx <fingerprint> (-with <fingerprint> | -break) [-actor <name>]
      Override the automatic transfer fold, which pairs the two sightings of one movement
      only when each names the other's account. -with forces a pair it missed, dropping the
      later sighting; -break keeps a line the fold wrongly paired. A later match supersedes.
  policy set -method <acb|fifo> [-account <a>]
  policy list
      Set the cost-basis method a sale's base is folded under. Without -account it sets the
      book-wide default; with one it overrides that account only, so a US account can run FIFO
      in the same book a Canadian default keeps on ACB. ACB blends every purchase into one
      average; FIFO draws each sale from the oldest lots first. The default is ACB. list shows
      the default and every override.
  report [-income | -balance | -gains] [-format text|html] [-account <text>] [-from <D>] [-to <D>] [-out <file>]
      The full picture: an income statement over the period (what was earned and spent, by
      account, with the net) and a balance sheet as of its end (assets held, liabilities owed,
      and net worth). -income or -balance shows just one; -gains shows the capital-gains
      schedule instead — a disposal per row (date, shares, proceeds, cost base, and realized
      gain) with the total gain, read off the cost-basis fold, for a tax year with -from/-to.
      -account narrows to accounts whose path contains the text, so "123 Main" reaches a
      property's income and its expenses at once, a client name reaches its consulting income,
      and a symbol reaches its disposals. Totals are per commodity, since a USD fee and CAD
      rent, or cash and shares, do not sum without a price. -format is text by default, or html
      for a page to print to PDF; -out writes to a file.
  review
      Print the whole books as JSON, in date order: every entry with its fingerprint, date,
      amount, description, payee, and postings (with prices). A generic report, the same fold
      the table renders. It never writes; whoever reads it answers back through categorize and
      rules set, and finds unplaced lines by their Uncategorized account, not a special queue.
  accounts set <account> -meta <k=v> ...
  accounts list
      Attach metadata to an account, merged per key. Well-known keys are name and address; an
      invoice reads them as the letterhead (the account the money moved through) and the
      customer block. It is authored knowledge, so it folds like a rule.
  receipt -tx <fingerprint> [-as invoice|receipt] [-format text|html] [-out <file>]
      Render one settled transaction as a printable invoice or receipt, found by its fingerprint:
      the biller is the transaction's account (its metadata is the letterhead), the bill-to is
      the payee, and the postings are the line items. It always reads PAID, because every line
      bookkeeper holds came off a statement, and it bills in the currency the posting names.
      -format is text by default, or html for a page to open and print to PDF; -out writes to a
      file. (This prints a document from money that already moved; invoice raise is the accrual
      invoice for money still owed.)
  export <connector> [-confirm]
      Write rent the books already booked out to a registered connector (see connectors register),
      so its paid/unpaid state stays current. Each rent deposit that a rule attributed to a
      lease (via -meta rentapp.lease=<id>) is recorded against that lease, keyed by the
      deposit's fingerprint so a repeat is a no-op. Without -confirm it is a dry run that
      prints what it would send.
  books [-format table|ledger] [-basis cash|accrual] [-since YYYY-MM-DD] [-stdout]
      Fold the log into a table (default), or regenerate .bookkeeper/books.ledger. -stdout
      writes the ledger to standard output instead of the store. -basis chooses the lens:
      cash (the default) books only money that moved; accrual also books every open invoice and
      bill, and lets the line that pays one clear its receivable or payable. The basis is a
      read-time choice over one log, so the same books read either way and switch with no rewrite.
      -since sets the effective date of that switch: on -basis accrual only invoices and bills
      dated on or after it are booked, so you can turn on accrual mid-year without retroactively
      accruing everything. An accrual before the date reads as cash (its payment books as income
      or expense when it lands). The caveat is a receivable open across the date: it is not shown
      until it is paid, when it books as cash.

INVOICES AND BILLS  (value recognized before its cash; only shown on -basis accrual)
  invoice raise -party <name> -amount <amt> -category <Income:...> [-account <a>] [-date <d>] [-currency <c>]
      Raise an invoice: revenue owed to you, earned and billed before the cash moves. It debits
      a receivable and credits income. -account names where it parks, defaulting to
      Assets:Receivable. -date is when the revenue was earned (default today), not when it will
      be paid. The amount is a positive magnitude. Raising the same invoice twice is a no-op,
      keyed by a fingerprint of its content, exactly as re-importing a statement is.
  bill receive -party <name> -amount <amt> -category <Expenses:...> [-account <a>] [-date <d>] [-currency <c>]
      Receive a bill: money you owe, the mirror of an invoice. It debits an expense and credits
      a payable, defaulting to Liabilities:Payable. Everything else matches invoice raise.
  invoice settle -id <fingerprint> (-tx <fingerprint> | -reopen)
  bill settle    -id <fingerprint> (-tx <fingerprint> | -reopen)
      Record that a bank line paid an invoice or bill, so on the accrual basis the cash clears
      the receivable or payable instead of booking the income or expense a second time (that
      was booked when the accrual was raised). A memo does not reliably name which accrual a
      line clears, so this pairing is recorded rather than guessed. -reopen unlinks it; a later
      settle supersedes.
  invoice void -id <fingerprint> [-why <reason>]
  bill void    -id <fingerprint> [-why <reason>]
      Drop an accrual that should not have been raised. The same verb as voiding a bad import:
      the raised fact stays in the log; a later fact supersedes it.
  invoice list
  bill list
      List the invoices or bills with their fingerprints, date, party, amount, category, parked
      account, and the line that settled each. An open one also lists CANDIDATES: the bank lines
      that plausibly settle it (same amount, within a few months, not already used elsewhere), so
      settling is picking a fingerprint from a short list rather than grepping the log. The offer
      is never applied on its own, because a memo does not prove which accrual a line clears.
  invoice aging [-as-of <YYYY-MM-DD>]
  bill aging    [-as-of <YYYY-MM-DD>]
      Age the open receivables (invoices) or payables (bills): what is still owed, oldest first,
      each bucketed by how long — current, 31-60, 61-90, 90+ — with a subtotal per bucket. -as-of
      ages against a date other than today. Settled and voided accruals have already left the fold,
      so only what is genuinely outstanding appears.

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

// Source is the input port: anything that yields normalized transactions. Every input -- a CSV or
// ledger file, or a connector's live fetch -- is one of these, so import records them all through
// one path, deduped by fingerprint. It is the ports-and-adapters seam for input, expressed as a
// function because that is all a source is: something you run to get lines.
type Source func() ([]model.Transaction, error)

// importCmd reads transactions into the log. Its argument is either a statement file, whose details
// are supplied inline (a CSV does not name its own account, currency, or columns), or the name of a
// registered connector, which already carries its account and currency and fetches its own lines.
// Either resolves to a Source and lands through the same importFrom.
func importCmd(args []string) error {
	arg, rest, err := firstArg(args, "a file or connector to import")
	if err != nil {
		return err
	}
	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	src, actor, err := sourceFor(s.Log, arg, rest)
	if err != nil {
		return err
	}
	return importFrom(s.Log, actor, src)
}

// importFrom runs a source and records what it yields. It is the one tail every import flows
// through, file or connector, so re-running any of them is a no-op on the lines already in the log.
func importFrom(log *eventlog.Log, actor string, src Source) error {
	txs, err := src()
	if err != nil {
		return err
	}
	result, err := books.Import(log, actor, txs)
	if err != nil {
		return err
	}
	fmt.Printf("%d transactions: %d imported, %d already in the log\n", len(txs), result.Imported, result.Skipped)
	return nil
}

// sourceFor resolves an import argument to a Source and the actor the log records it under. A file's
// kind is its extension; anything else is a registered connector. A ledger file's own categorization
// is not carried in: the line is imported raw and the rules place it, so the books stay a fold.
func sourceFor(log *eventlog.Log, arg string, rest []string) (Source, string, error) {
	label := "statement:" + filepath.Base(arg)
	switch ext := strings.ToLower(filepath.Ext(arg)); ext {
	case ".csv":
		m, err := csvMapping(rest)
		if err != nil {
			return nil, "", err
		}
		return fileSource(arg, func(r io.Reader) ([]model.Transaction, error) { return source.ReadCSV(r, m) }), label, nil
	case ".ledger":
		return fileSource(arg, source.ReadLedger), label, nil
	default:
		c, ok, err := books.ConnectorByName(log, arg)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			return nil, "", fmt.Errorf("import: %q is not a .csv or .ledger file, nor a registered connector (see `connectors list`)", arg)
		}
		fetch, err := fetcherFor(c.Kind)
		if err != nil {
			return nil, "", err
		}
		return func() ([]model.Transaction, error) { return fetch(c) }, "connector:" + c.Name, nil
	}
}

// fileSource opens a file when the source is run and reads it with the given reader, so the handle
// lives no longer than the read.
func fileSource(path string, read func(io.Reader) ([]model.Transaction, error)) Source {
	return func() ([]model.Transaction, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return read(f)
	}
}

// csvMapping parses the inline flags a CSV import needs, since a CSV does not name its own account,
// currency, or columns.
func csvMapping(args []string) (source.CSV, error) {
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
		return source.CSV{}, err
	}
	switch {
	case m.Account == "":
		return source.CSV{}, fmt.Errorf("-account is required: which account's statement is this?")
	case m.Currency == "":
		return source.CSV{}, fmt.Errorf("-currency is required")
	case m.Amount == "" && m.Debit == "" && m.Credit == "":
		return source.CSV{}, fmt.Errorf("-amount, or -debit and -credit, is required")
	}
	return m, nil
}

// connectorSet dispatches `connectors register|rm|list`.
func connectorSet(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("connectors needs register, rm, or list")
	}
	switch args[0] {
	case "register":
		return connectorRegister(args[1:])
	case "rm":
		return connectorRemove(args[1:])
	case "list":
		return connectorList(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown connectors subcommand %q", args[0])
	}
}

func connectorRegister(args []string) error {
	name, rest, err := firstArg(args, "a name for the connector, e.g. rent")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("connectors register", flag.ExitOnError)
	var c books.Connector
	fs.StringVar(&c.Kind, "kind", "rentapp", "which system: rentapp (export), or a bank to import from with no free API: rbc, simplii, pcfinancial")
	fs.StringVar(&c.URL, "url", "", "the connector's base URL")
	fs.StringVar(&c.TokenEnv, "token-env", "", "the environment variable holding its bearer token")
	fs.StringVar(&c.Account, "account", "", "the ledger account its transactions land in")
	fs.StringVar(&c.Currency, "currency", "CAD", "default commodity for a fetched line that carries none of its own (a brokerage line's own currency wins)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	c.Name = name

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.RegisterConnector(log, "human", c); err != nil {
		return err
	}
	fmt.Printf("connector %s\n", c.Name)
	return nil
}

func connectorRemove(args []string) error {
	name, _, err := firstArg(args, "the connector to forget")
	if err != nil {
		return err
	}
	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.RemoveConnector(log, "human", name); err != nil {
		return err
	}
	fmt.Printf("removed connector %s\n", name)
	return nil
}

func connectorList(args []string) error {
	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	set, err := books.Connectors(log)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tURL\tACCOUNT\tCURRENCY\tTOKEN-ENV")
	for _, c := range set {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", c.Name, c.Kind, c.URL, c.Account, c.Currency, c.TokenEnv)
	}
	return w.Flush()
}

// policyCmd dispatches `policy set|list`, the cost-basis method a sale's base is folded under.
func policyCmd(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("policy needs set or list")
	}
	switch args[0] {
	case "set":
		return policySetOne(args[1:])
	case "list":
		return policyList(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown policy subcommand %q", args[0])
	}
}

func policySetOne(args []string) error {
	fs := flag.NewFlagSet("policy set", flag.ExitOnError)
	method := fs.String("method", "", "the cost-basis method: acb or fifo")
	account := fs.String("account", "", "the account this applies to; omit to set the book default")
	actor := fs.String("actor", "human", "who is setting this; the log records who decided")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *method == "" {
		return fmt.Errorf("a method is required: acb or fifo")
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.SetPolicy(log, *actor, *account, *method); err != nil {
		return err
	}
	if *account == "" {
		fmt.Printf("book default cost basis is now %s\n", *method)
	} else {
		fmt.Printf("%s cost basis is now %s\n", *account, *method)
	}
	return nil
}

func policyList(args []string) error {
	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	set, err := books.Policies(log)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ACCOUNT\tMETHOD")
	for _, p := range set.PolicyList() {
		account := p.Account
		if account == books.BookDefault {
			account = "(book default)"
		}
		fmt.Fprintf(w, "%s\t%s\n", account, p.Method)
	}
	return w.Flush()
}

// accountCmd dispatches `accounts set|list`, the metadata an account carries (a letterhead address,
// a customer's mailing address, a display name) that a document like an invoice reads.
func accountCmd(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("accounts needs set or list")
	}
	switch args[0] {
	case "set":
		return accountSetMeta(args[1:])
	case "list":
		return accountList(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown accounts subcommand %q", args[0])
	}
}

func accountSetMeta(args []string) error {
	account, rest, err := firstArg(args, "the account to set metadata on")
	if err != nil {
		return err
	}
	meta := metaFlag{}
	fs := flag.NewFlagSet("accounts set", flag.ExitOnError)
	fs.Var(&meta, "meta", "key=value, repeatable; e.g. -meta name=\"Excite Creative\" -meta address=\"123 Main St\"")
	actor := fs.String("actor", "human", "who is setting this; the log records who decided")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if len(meta) == 0 {
		return fmt.Errorf("give at least one -meta key=value to set")
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.SetAccountMeta(log, *actor, account, meta); err != nil {
		return err
	}
	fmt.Printf("set %d field(s) on %s\n", len(meta), account)
	return nil
}

func accountList(args []string) error {
	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	all, err := books.AccountMeta(log)
	if err != nil {
		return err
	}
	accounts := make([]string, 0, len(all))
	for a := range all {
		accounts = append(accounts, a)
	}
	sort.Strings(accounts)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ACCOUNT\tKEY\tVALUE")
	for _, a := range accounts {
		keys := make([]string, 0, len(all[a]))
		for k := range all[a] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s\t%s\t%s\n", a, k, strings.ReplaceAll(all[a][k], "\n", " / "))
		}
	}
	return w.Flush()
}

// ruleSet dispatches `rules set|rm|mv|list`.
func ruleSet(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("rules needs set, rm, mv, or list")
	}
	switch args[0] {
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
// namespaced identifier a connector reads (e.g. rentapp.lease); the value is kept verbatim.
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

// ruleSetOne is the one verb for authoring a rule: it adds a pattern not yet known, or changes the
// one already matching it. There is no separate add, because a pattern is a rule's identity and
// "make this pattern say X" is the same intent whether or not it existed.
func ruleSetOne(args []string) error {
	fs := flag.NewFlagSet("rules set", flag.ExitOnError)
	var r rules.Rule
	var meta metaFlag
	fs.StringVar(&r.Match, "match", "", "case-insensitive pattern to match the description")
	fs.StringVar(&r.Category, "category", "", "account to post the line to")
	fs.StringVar(&r.Payee, "payee", "", "payee to record on the entry")
	fs.Var(&meta, "meta", "key=value carried onto the entry, repeatable, e.g. rentapp.lease=31")
	before := fs.String("before", "", "on a new rule, place it ahead of the one matching this pattern")
	why := fs.String("why", "", "why the rule changed; changing one reclassifies every line it matched")
	actor := fs.String("actor", "human", "who is authoring this rule; the log records who decided")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if r.Match == "" {
		return fmt.Errorf("-match is required")
	}

	// Only the fields you name change, so setting a category does not silently blank the payee.
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	if len(meta) > 0 {
		r.Metadata = meta
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := upsertRule(log, r, provided, *before, *why, *actor); err != nil {
		return err
	}
	fmt.Printf("rule %q\n", r.Match)
	return nil
}

// upsertRule adds r, or changes the rule already matching its pattern. On a change only the named
// fields move, and metadata merges per key rather than replacing the bag, so naming one key leaves
// the others. On a new rule the given fields stand and before places it. actor records who decided,
// so a rule authored by an agent is told apart from one a person wrote.
func upsertRule(log *eventlog.Log, r rules.Rule, provided map[string]bool, before, why, actor string) error {
	current, err := books.Rules(log)
	if err != nil {
		return err
	}
	for _, existing := range current {
		if existing.Match != r.Match {
			continue
		}
		merged := existing
		if provided["category"] {
			merged.Category = r.Category
		}
		if provided["payee"] {
			merged.Payee = r.Payee
		}
		if provided["meta"] {
			merged.Metadata = mergeMeta(merged.Metadata, r.Metadata)
		}
		return books.SetRule(log, actor, why, merged)
	}
	return books.AddRule(log, actor, r, before)
}

// mergeMeta overlays new keys onto the existing bag without dropping the untouched ones.
func mergeMeta(base, overlay map[string]string) map[string]string {
	if len(overlay) == 0 {
		return base
	}
	out := make(map[string]string, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
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

// postingsFor turns the flags into the postings to assert. A -category names one account and takes
// the whole line in its own commodity; -post entries are spelled out, each a quantity with an
// optional commodity and an optional "@@" total price, so a share bought with cash reads
// "Assets:Brokerage:AAPL=10 AAPL @@ 1000.00 USD". A bare number keeps the line's commodity, so an
// ordinary split is unchanged. The line's amount supplies that fallback commodity.
func postingsFor(category string, split splitFlag, line model.Amount) ([]model.Posting, error) {
	if category != "" {
		// The common case: the whole line to one account, in the line's amount with the opposite sign.
		return []model.Posting{{Account: category, Amount: line.Negate()}}, nil
	}

	var post []model.Posting
	for _, rp := range split {
		amount, cost, err := model.ParsePosting(rp.quantity, line.Commodity)
		if err != nil {
			return nil, err
		}
		post = append(post, model.Posting{Account: rp.account, Amount: amount, Cost: cost})
	}
	return post, nil
}

// disposalsFor turns the -sell flags into the holdings a sale disposes of. Each names an account
// and a positive share quantity with its commodity, e.g. "Assets:Brokerage:AAPL=10 AAPL". A sale
// carries no price: the cost base is folded from the account's purchases, not restated here.
func disposalsFor(sell splitFlag) ([]model.Posting, error) {
	var post []model.Posting
	for _, rp := range sell {
		amount, cost, err := model.ParsePosting(rp.quantity, "")
		if err != nil {
			return nil, err
		}
		if cost != nil {
			return nil, fmt.Errorf("sell %q: a sale takes no price; its cost base is folded from your purchases", rp.account)
		}
		post = append(post, model.Posting{Account: rp.account, Amount: amount})
	}
	return post, nil
}

// categorize records a human's answer for one line: its category, a split across several, or a sale
// that disposes of shares and books the gain.
func categorize(args []string) error {
	var split, sell splitFlag
	fs := flag.NewFlagSet("categorize", flag.ExitOnError)
	txID := fs.String("tx", "", "the transaction fingerprint to categorize")
	category := fs.String("category", "", "post the whole line to this one account")
	payee := fs.String("payee", "", "the payee to record on the entry")
	why := fs.String("why", "", "why this line is categorized so; recorded with the assertion")
	gain := fs.String("gain", "", "on a sale, the account its capital gain or loss lands in, e.g. Income:Capital Gains")
	actor := fs.String("actor", "human", "who is categorizing; the log records who decided")
	fs.Var(&split, "post", "account=amount, repeatable; amount may carry a commodity and an @@ total price, e.g. \"Assets:Brokerage:AAPL=10 AAPL @@ 1000.00 USD\"")
	fs.Var(&sell, "sell", "account=quantity, repeatable; the shares this line sold, e.g. \"Assets:Brokerage:AAPL=10 AAPL\", paired with -gain")
	if err := fs.Parse(args); err != nil {
		return err
	}

	isSale := len(sell) > 0 || *gain != ""
	switch {
	case *txID == "":
		fs.Usage()
		return fmt.Errorf("tx is required")
	case isSale && (*category != "" || len(split) > 0):
		return fmt.Errorf("a sale is -sell with -gain, not mixed with -category or -post")
	case !isSale && *category == "" && len(split) == 0:
		fs.Usage()
		return fmt.Errorf("one of -category, -post, or -sell is required")
	case *category != "" && len(split) > 0:
		return fmt.Errorf("give -category or -post, not both")
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	if isSale {
		disposals, err := disposalsFor(sell)
		if err != nil {
			return err
		}
		if err := books.Sell(s.Log, *actor, *why, *txID, *payee, *gain, disposals); err != nil {
			return err
		}
		fmt.Printf("categorized %s\n", *txID)
		return nil
	}

	// The line's own commodity is what a posting is denominated in, so it is fetched before the
	// postings are built and the caller never restates it.
	tx, err := books.Transaction(s.Log, *txID)
	if err != nil {
		return err
	}

	post, err := postingsFor(*category, split, tx.Amount)
	if err != nil {
		return err
	}

	if err := books.Categorize(s.Log, *actor, *why, *txID, *payee, post); err != nil {
		return err
	}
	fmt.Printf("categorized %s\n", *txID)
	return nil
}

// voidCmd annuls a bad imported line, the way to undo a bad import.
func voidCmd(args []string) error {
	fs := flag.NewFlagSet("void", flag.ExitOnError)
	txID := fs.String("tx", "", "the transaction fingerprint to void")
	why := fs.String("why", "", "why the line is annulled; recorded with the void")
	actor := fs.String("actor", "human", "who is voiding; the log records who decided")
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

	if err := books.VoidTransaction(s.Log, *actor, *why, *txID); err != nil {
		return err
	}
	fmt.Printf("voided %s\n", *txID)
	return nil
}

// match overrides the automatic transfer fold for one line: force a pairing it missed (mutual naming
// is the only thing it recognises), or break one it wrongly made.
func match(args []string) error {
	fs := flag.NewFlagSet("match", flag.ExitOnError)
	txID := fs.String("tx", "", "the transaction to match")
	with := fs.String("with", "", "the other sighting; the two are one movement and the later is dropped")
	brk := fs.Bool("break", false, "this line is not a duplicate; keep it")
	actor := fs.String("actor", "human", "who is matching; the log records who decided")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *txID == "" {
		return fmt.Errorf("-tx is required")
	}
	if *brk == (*with != "") {
		return fmt.Errorf("give -with <tx> to force a pair, or -break to keep a line, not both or neither")
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.Match(log, *actor, *txID, *with, !*brk); err != nil {
		return err
	}
	if *brk {
		fmt.Printf("broke the match on %s\n", *txID)
	} else {
		fmt.Printf("matched %s with %s\n", *txID, *with)
	}
	return nil
}

// invoiceCmd dispatches `invoice raise|settle|void|list`. An invoice is a first-class thing you do,
// so it is its own namespace, the way rules and connectors are, rather than a subtype of a more
// abstract verb.
func invoiceCmd(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("invoice needs raise, settle, void, or list")
	}
	switch args[0] {
	case "raise":
		return invoiceRaise(args[1:])
	case "settle":
		return invoiceSettle(args[1:])
	case "void":
		return invoiceVoid(args[1:])
	case "list":
		return invoiceList(args[1:])
	case "aging":
		return invoiceAging(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown invoice subcommand %q", args[0])
	}
}

// invoiceRaise records an invoice: revenue earned and billed before its cash moves.
func invoiceRaise(args []string) error {
	fs := flag.NewFlagSet("invoice raise", flag.ExitOnError)
	party := fs.String("party", "", "the customer billed")
	amount := fs.String("amount", "", "the magnitude owed, e.g. 1600.00")
	currency := fs.String("currency", "CAD", "the currency of the amount")
	category := fs.String("category", "", "the Income account the revenue is recognized in")
	account := fs.String("account", "", "where it parks until paid; defaults to Assets:Receivable")
	date := fs.String("date", "", "when the revenue was earned (YYYY-MM-DD); defaults to today")
	why := fs.String("why", "", "why this invoice was raised; recorded with it")
	actor := fs.String("actor", "human", "who is raising it; the log records who decided")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case *party == "":
		return fmt.Errorf("-party is required")
	case *amount == "":
		return fmt.Errorf("-amount is required")
	case *category == "":
		return fmt.Errorf("-category is required")
	}

	amt, err := model.NewAmount(*amount, *currency)
	if err != nil {
		return err
	}
	when := time.Now()
	if *date != "" {
		if when, err = time.Parse("2006-01-02", *date); err != nil {
			return fmt.Errorf("-date %q is not YYYY-MM-DD", *date)
		}
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	inv, added, err := books.Raise(log, *actor, *why, books.Invoice{
		Date: when, Party: *party, Amount: amt, Category: *category, Account: *account,
	})
	if err != nil {
		return err
	}
	if added {
		fmt.Printf("invoice %s\n", inv.ID)
	} else {
		fmt.Printf("invoice %s already recorded\n", inv.ID)
	}
	return nil
}

// invoiceSettle links an invoice to the bank line that paid it, or reopens it. The pairing is
// recorded rather than guessed, because a deposit's memo does not reliably name which invoice it
// clears.
func invoiceSettle(args []string) error {
	fs := flag.NewFlagSet("invoice settle", flag.ExitOnError)
	id := fs.String("id", "", "the invoice fingerprint to settle")
	txID := fs.String("tx", "", "the bank line that paid it")
	reopen := fs.Bool("reopen", false, "unlink the invoice from its paying line")
	actor := fs.String("actor", "human", "who is settling; the log records who decided")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("-id is required")
	}
	if *reopen == (*txID != "") {
		return fmt.Errorf("give -tx <fingerprint> to settle, or -reopen to unlink, not both or neither")
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.SettleInvoice(log, *actor, *id, *txID); err != nil {
		return err
	}
	if *reopen {
		fmt.Printf("reopened %s\n", *id)
	} else {
		fmt.Printf("settled %s with %s\n", *id, *txID)
	}
	return nil
}

// invoiceVoid drops an invoice that should not have been raised.
func invoiceVoid(args []string) error {
	fs := flag.NewFlagSet("invoice void", flag.ExitOnError)
	id := fs.String("id", "", "the invoice fingerprint to void")
	why := fs.String("why", "", "why it is voided; recorded with the void")
	actor := fs.String("actor", "human", "who is voiding; the log records who decided")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("-id is required")
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.VoidInvoice(log, *actor, *why, *id); err != nil {
		return err
	}
	fmt.Printf("voided %s\n", *id)
	return nil
}

// invoiceList prints the open invoices and the line that settled each, if any.
func invoiceList(args []string) error {
	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	invs, err := books.Invoices(log)
	if err != nil {
		return err
	}
	settled, err := books.InvoiceSettlements(log)
	if err != nil {
		return err
	}
	candidates, err := books.SettlementCandidates(log)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tDATE\tPARTY\tAMOUNT\tCATEGORY\tACCOUNT\tSETTLED BY\tCANDIDATES")
	for _, inv := range invs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			inv.ID, inv.Date.Format("2006-01-02"), inv.Party, inv.Amount, inv.Category, inv.Account,
			settled[inv.ID], strings.Join(candidates[inv.ID], " "))
	}
	return w.Flush()
}

// billCmd dispatches `bill receive|settle|void|list`. A bill is the mirror of an invoice — money you
// owe rather than money owed to you — and is its own first-class namespace the same way.
func billCmd(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("bill needs receive, settle, void, or list")
	}
	switch args[0] {
	case "receive":
		return billReceive(args[1:])
	case "settle":
		return billSettle(args[1:])
	case "void":
		return billVoid(args[1:])
	case "list":
		return billList(args[1:])
	case "aging":
		return billAging(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown bill subcommand %q", args[0])
	}
}

// billReceive records a bill: an expense incurred and billed before its cash leaves.
func billReceive(args []string) error {
	fs := flag.NewFlagSet("bill receive", flag.ExitOnError)
	party := fs.String("party", "", "the vendor billing you")
	amount := fs.String("amount", "", "the magnitude owed, e.g. 142.03")
	currency := fs.String("currency", "CAD", "the currency of the amount")
	category := fs.String("category", "", "the Expenses account the expense is recognized in")
	account := fs.String("account", "", "where it parks until paid; defaults to Liabilities:Payable")
	date := fs.String("date", "", "when the expense was incurred (YYYY-MM-DD); defaults to today")
	why := fs.String("why", "", "why this bill was received; recorded with it")
	actor := fs.String("actor", "human", "who is recording it; the log records who decided")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case *party == "":
		return fmt.Errorf("-party is required")
	case *amount == "":
		return fmt.Errorf("-amount is required")
	case *category == "":
		return fmt.Errorf("-category is required")
	}

	amt, err := model.NewAmount(*amount, *currency)
	if err != nil {
		return err
	}
	when := time.Now()
	if *date != "" {
		if when, err = time.Parse("2006-01-02", *date); err != nil {
			return fmt.Errorf("-date %q is not YYYY-MM-DD", *date)
		}
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	b, added, err := books.ReceiveBill(log, *actor, *why, books.Bill{
		Date: when, Party: *party, Amount: amt, Category: *category, Account: *account,
	})
	if err != nil {
		return err
	}
	if added {
		fmt.Printf("bill %s\n", b.ID)
	} else {
		fmt.Printf("bill %s already recorded\n", b.ID)
	}
	return nil
}

// billSettle links a bill to the bank line that paid it, or reopens it.
func billSettle(args []string) error {
	fs := flag.NewFlagSet("bill settle", flag.ExitOnError)
	id := fs.String("id", "", "the bill fingerprint to settle")
	txID := fs.String("tx", "", "the bank line that paid it")
	reopen := fs.Bool("reopen", false, "unlink the bill from its paying line")
	actor := fs.String("actor", "human", "who is settling; the log records who decided")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("-id is required")
	}
	if *reopen == (*txID != "") {
		return fmt.Errorf("give -tx <fingerprint> to settle, or -reopen to unlink, not both or neither")
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.SettleBill(log, *actor, *id, *txID); err != nil {
		return err
	}
	if *reopen {
		fmt.Printf("reopened %s\n", *id)
	} else {
		fmt.Printf("settled %s with %s\n", *id, *txID)
	}
	return nil
}

// billVoid drops a bill that should not have been received.
func billVoid(args []string) error {
	fs := flag.NewFlagSet("bill void", flag.ExitOnError)
	id := fs.String("id", "", "the bill fingerprint to void")
	why := fs.String("why", "", "why it is voided; recorded with the void")
	actor := fs.String("actor", "human", "who is voiding; the log records who decided")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("-id is required")
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.VoidBill(log, *actor, *why, *id); err != nil {
		return err
	}
	fmt.Printf("voided %s\n", *id)
	return nil
}

// billList prints the open bills and the line that settled each, if any.
func billList(args []string) error {
	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	bills, err := books.Bills(log)
	if err != nil {
		return err
	}
	settled, err := books.BillSettlements(log)
	if err != nil {
		return err
	}
	candidates, err := books.SettlementCandidates(log)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tDATE\tPARTY\tAMOUNT\tCATEGORY\tACCOUNT\tSETTLED BY\tCANDIDATES")
	for _, b := range bills {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			b.ID, b.Date.Format("2006-01-02"), b.Party, b.Amount, b.Category, b.Account,
			settled[b.ID], strings.Join(candidates[b.ID], " "))
	}
	return w.Flush()
}

// agingAsOf parses the -as-of date an aging report is computed against, defaulting to today.
func agingAsOf(name string, args []string) (time.Time, error) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	asOf := fs.String("as-of", "", "age as of this date (YYYY-MM-DD); defaults to today")
	if err := fs.Parse(args); err != nil {
		return time.Time{}, err
	}
	if *asOf == "" {
		return time.Now(), nil
	}
	t, err := time.Parse("2006-01-02", *asOf)
	if err != nil {
		return time.Time{}, fmt.Errorf("-as-of %q is not YYYY-MM-DD", *asOf)
	}
	return t, nil
}

// invoiceAging prints the receivables aging: open invoices bucketed by how long they have been owed.
func invoiceAging(args []string) error {
	asOf, err := agingAsOf("invoice aging", args)
	if err != nil {
		return err
	}
	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	rows, err := books.InvoiceAging(log, asOf)
	if err != nil {
		return err
	}
	return printAging("receivables", rows)
}

// billAging prints the payables aging: open bills bucketed by how long you have owed them.
func billAging(args []string) error {
	asOf, err := agingAsOf("bill aging", args)
	if err != nil {
		return err
	}
	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	rows, err := books.BillAging(log, asOf)
	if err != nil {
		return err
	}
	return printAging("payables", rows)
}

// printAging renders an aging report: a line per open accrual, oldest first, then a subtotal per
// bucket. Subtotals sum within a commodity; a mixed-commodity book keeps its counts honest even if a
// total cannot be formed, which is the same restraint the ledger applies to amounts it cannot sum.
func printAging(kind string, rows []books.AgedAccrual) error {
	if len(rows) == 0 {
		fmt.Printf("no open %s\n", kind)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tDATE\tPARTY\tAMOUNT\tDAYS\tBUCKET")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
			r.ID, r.Date.Format("2006-01-02"), r.Party, r.Amount, r.Days, r.Bucket)
	}
	if err := w.Flush(); err != nil {
		return err
	}

	order := []string{"current", "31-60", "61-90", "90+"}
	totals := map[string]model.Amount{}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.Bucket]++
		if cur, ok := totals[r.Bucket]; ok {
			if sum, err := cur.Add(r.Amount); err == nil {
				totals[r.Bucket] = sum
			}
		} else {
			totals[r.Bucket] = r.Amount
		}
	}

	fmt.Println()
	s := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(s, "BUCKET\tTOTAL\tCOUNT")
	for _, b := range order {
		if counts[b] > 0 {
			fmt.Fprintf(s, "%s\t%s\t%d\n", b, totals[b], counts[b])
		}
	}
	return s.Flush()
}

func renderBooks(args []string) error {
	fs := flag.NewFlagSet("books", flag.ExitOnError)
	format := fs.String("format", "table", "output format: table or ledger")
	basis := fs.String("basis", "cash", "accounting basis: cash or accrual")
	since := fs.String("since", "", "on -basis accrual, book only invoices/bills dated on or after this (YYYY-MM-DD)")
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

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	txs, entries, err := books.LedgerBasisSince(s.Log, books.Basis(*basis), effective)
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
