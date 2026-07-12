// Command bookkeeper turns bank and card statements into a set of books.
//
// A directory holds a set of books the way it holds a git repository, marked by `.bkpr` and
// found by walking up. `import` records what a statement said, once per line, into the append-only
// log inside it. `books` folds that log back out through a rule set and renders it. Where the
// rules run out of knowledge the account path stops at Uncategorized rather than guessing, and the
// agent operating the tool answers those through the same commands a person would. bookkeeper is
// built to be driven by an agent; it holds no model of its own.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

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
	case "reset":
		err = resetCmd(os.Args[2:])
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
	case "policy":
		err = policyCmd(os.Args[2:])
	case "accounts":
		err = accountCmd(os.Args[2:])
	case "reconcile":
		err = reconcileCmd(os.Args[2:])
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
	case "version":
		versionCmd(os.Stdout)
	case "help", "-h", "--help":
		if len(os.Args) > 2 {
			err = helpTopic(os.Stdout, os.Args[2])
		} else {
			usage()
		}
	default:
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "bookkeeper: %v\n", err)
		os.Exit(1)
	}
}

// palette carries the ANSI styles the usage screen paints with, or empty strings when color is off,
// so the same render runs to a terminal in color and to a pipe in bare text. It is comparable, so an
// off palette is exactly the zero value.
type palette struct {
	title   string // the tool name
	heading string // a section header
	command string // a command's verb
	dim     string // the [ optional ] groups, and the bkpr prefix
	reset   string
}

// colorPalette is the styling a real terminal gets: a bold title, bold-cyan section headers, cyan
// verbs, and dimmed optionals so the required arguments are what stands out.
var colorPalette = palette{
	title:   "\x1b[1m",
	heading: "\x1b[1;36m",
	command: "\x1b[36m",
	dim:     "\x1b[2m",
	reset:   "\x1b[0m",
}

// paletteFor chooses styling for f: none when NO_COLOR is set (the honored opt-out) or when f is not
// a terminal (a pipe, a redirect, an agent reading the screen), color otherwise.
func paletteFor(f *os.File) palette {
	if os.Getenv("NO_COLOR") != "" || !isTerminal(f) {
		return palette{}
	}
	return colorPalette
}

// usageLine is one invocation: the verb to run (styled) and the arguments it takes.
type usageLine struct {
	verb string
	args string
}

// usageSection is a titled run of commands, so the usage screen carries the reference's shape rather
// than one flat wall of lines.
type usageSection struct {
	title string
	lines []usageLine
}

var usageSections = []usageSection{
	{"SETUP", []usageLine{
		{"init", "[dir]"},
		{"reset", "[-confirm]"},
		{"connectors register", "<name> -kind <rentapp|rbc|simplii|pcfinancial> -url <url> -token-env <ENV> -account <a> [-currency <c>]"},
		{"connectors rm", "<name>"},
		{"connectors list", ""},
	}},
	{"RULES", []usageLine{
		{"rules set", "<re> [-category <account>] [-payee <name>] [-meta <k=v> ...] [-before <re>] [-why <reason>] [-actor <name>]"},
		{"rules rm", "<re>"},
		{"rules mv", "<re> [-before <re>]"},
		{"rules list", ""},
	}},
	{"BOOKKEEPING", []usageLine{
		{"import", "<file.csv> -account <a> -currency <c> (-amount <col> | -debit <col> -credit <col>) [-date <col> -description <col> -date-format <layout>]"},
		{"import", "<file.ledger>"},
		{"import", "<book.jsonl>"},
		{"import", "<file> -format csv|ledger|jsonl"},
		{"import", "<connector> [-relogin]"},
		{"categorize", "<fingerprint> (-category <account> | -post <account>=<amount> ...) [-payee <name>] [-why <reason>] [-actor <name>]"},
		{"void", "<fingerprint> [-why <reason>] [-actor <name>]"},
		{"match", "<fingerprint> (-with <fingerprint> | -break) [-actor <name>]"},
		{"export", "<connector> [-confirm]"},
		{"books", "[-format table|json|ledger] [-basis cash|accrual] [-since <YYYY-MM-DD>] [-account <re> ...] [-from <YYYY-MM-DD>] [-to <YYYY-MM-DD>] [-stdout]"},
	}},
	{"INVOICES AND BILLS", []usageLine{
		{"invoice raise", "-party <name> -amount <amt> -category <account> [-account <a>] [-date <YYYY-MM-DD>] [-currency <c>] [-why <reason>] [-actor <name>]"},
		{"invoice settle", "<fingerprint> (-tx <fingerprint> | -reopen) [-actor <name>]"},
		{"invoice void", "<fingerprint> [-why <reason>] [-actor <name>]"},
		{"invoice list", ""},
		{"invoice aging", "[-as-of <YYYY-MM-DD>]"},
		{"bill receive", "-party <name> -amount <amt> -category <account> [-account <a>] [-date <YYYY-MM-DD>] [-currency <c>] [-why <reason>] [-actor <name>]"},
		{"bill settle", "<fingerprint> (-tx <fingerprint> | -reopen) [-actor <name>]"},
		{"bill void", "<fingerprint> [-why <reason>] [-actor <name>]"},
		{"bill list", ""},
		{"bill aging", "[-as-of <YYYY-MM-DD>]"},
	}},
	{"POLICIES AND DOCUMENTS", []usageLine{
		{"policy set", "-method <acb|fifo> [-account <a>] [-actor <name>]"},
		{"policy list", ""},
		{"accounts set", "<account> -meta <k=v> ... [-actor <name>]"},
		{"accounts list", ""},
		{"reconcile", ""},
		{"receipt", "-tx <fingerprint> [-as invoice|receipt] [-format text|html] [-out <file>]"},
		{"report", "[-income | -balance | -gains] [-basis cash|accrual] [-format text|html] [-account <text>] [-from <D>] [-to <D>] [-out <file>]"},
	}},
	{"MORE", []usageLine{
		{"help", "[command]"},
		{"docs", ""},
		{"version", ""},
	}},
}

const usageIntro = `Every command finds the nearest .bkpr directory by walking up, as git does.
Run "bkpr help <command>" for one command, "bkpr docs" for the full reference.
The thing a command acts on is its first argument; flags assert facts about it.
Wherever a fingerprint is taken, a unique prefix is enough, as with a git hash.
`

// dimOptionals wraps each [ ... ] group in the dim style, so the optional flags recede and the
// required arguments are what the eye lands on. Brackets do not nest in these synopses.
func dimOptionals(s string, p palette) string {
	if p.dim == "" {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '[':
			b.WriteString(p.dim)
			b.WriteRune(r)
		case ']':
			b.WriteRune(r)
			b.WriteString(p.reset)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// writeUsage renders the grouped command list to w in the given palette: bold title, one section
// header per group, and every verb aligned into a column so its arguments line up.
func writeUsage(w io.Writer, p palette) {
	fmt.Fprintf(w, "%sbookkeeper (bkpr)%s - turn statements into books\n\n", p.title, p.reset)
	fmt.Fprint(w, usageIntro)

	var width int
	for _, s := range usageSections {
		for _, l := range s.lines {
			if len(l.verb) > width {
				width = len(l.verb)
			}
		}
	}

	for _, s := range usageSections {
		fmt.Fprintf(w, "\n%s%s%s\n", p.heading, s.title, p.reset)
		for _, l := range s.lines {
			line := fmt.Sprintf("  %sbkpr%s %s%-*s%s %s",
				p.dim, p.reset, p.command, width, l.verb, p.reset, dimOptionals(l.args, p))
			fmt.Fprintln(w, strings.TrimRight(line, " "))
		}
	}
}

func usage() {
	writeUsage(os.Stderr, paletteFor(os.Stderr))
}

// docTopic is one command's block of the reference: the names `help` answers to, and the text
// `docs` prints. One source feeds both, so the two can never drift apart.
type docTopic struct {
	names []string
	text  string
}

// docGroup is a titled run of topics, so `docs` keeps the reference's shape.
type docGroup struct {
	title  string
	topics []docTopic
}

const docsPreamble = `bookkeeper (bkpr) - turn bank and card statements into a plain-text double-entry ledger.

A set of books lives in a .bkpr directory, found by walking up from the current
directory the way git finds .git. The log inside it (log.jsonl) is the book of record;
everything else, including the ledger artifact, is a fold over it and is regenerated.

One grammar throughout: the thing a command acts on is its first argument (a file, a
connector, a rule's pattern, a fingerprint); flags assert facts about it.

`

const docsFooter = `Wherever a fingerprint is taken - as a command's first argument, or as -with or -tx -
a unique prefix of at least four characters is enough, as with a git hash; books and
the list commands print the fingerprints to quote. See the README for the design.
`

var reference = []docGroup{
	{"SETUP", []docTopic{
		{[]string{"init"}, `  init [dir]
      Create a set of books in dir (default: here).
`},
		{[]string{"reset"}, `  reset [-confirm]
      Empty the book of record: every event discarded, the artifact removed, the directory
      still a book. This is the start-over verb and the only one that destroys history, so
      without -confirm it is a dry run that says what would be lost. It is not how a mistake
      is corrected - a wrong line is void, a wrong rule is rules rm, a wrong settle is
      -reopen, each a later fact that supersedes - and not how a bad batch is unwound: every
      write is a pure append, so git restore .bkpr/log.jsonl rolls the book back to any
      committed point. After a reset the old log is recoverable only from git.
`},
		{[]string{"connectors"}, `  connectors register <name> -kind <kind> -url <url> -token-env <ENV> -account <a> [-currency <c>]
      Register a live connector. Kinds: rentapp (export), and the banks imported from --
      rbc, simplii, pcfinancial. No secret is stored: -token-env names where the credential
      lives, read when the connector is used. For a bank that credential is a saved browser
      session rather than a token, so connectors that share a login share a -token-env and
      thus one sign-in (every RBC account, say). A connector is bidirectional in principle:
      export writes to it, import reads from it. Registering one does not itself move data.
  connectors rm <name>            Forget a connector.
  connectors list                 Show the registered connectors.
`},
	}},
	{"RULES  (deterministic categorization; first matching rule wins per field)", []docTopic{
		{[]string{"rules"}, `  rules set <re> [-category <account>] [-payee <name>] [-meta <k=v> ...] [-before <re>] [-why <reason>]
      Add a rule, or change one already matching this pattern. On an existing rule only the
      fields you name change, and since that reclassifies every past line it matched, it
      takes a -why. A new rule lands last unless -before places it ahead of another. Account
      paths are free-form and may stop at Uncategorized wherever knowledge runs out. -meta
      attaches opaque key=value pairs (repeatable) that a connector reads by name, e.g.
      -meta rentapp.lease=31 tells the export which lease a matching rent deposit belongs to.
      Either way it reports how many lines the books reclassified, so a pattern that catches
      nothing (or too much) is visible the moment it is written.
  rules rm  <re>                  Remove a rule.
  rules mv  <re> [-before <re>]   Reorder a rule (-before omitted moves it last).
  rules list                      Show the rules in order.
`},
	}},
	{"BOOKKEEPING", []docTopic{
		{[]string{"import"}, `  import <file.csv> -account <a> -currency <c> (-amount <col> | -debit <col> -credit <col>)
                    [-date <col>] [-description <col>] [-date-format <layout>]
  import <file.ledger>
  import <file> -format csv|ledger|jsonl
      Read transactions in. A file is a one-time input: a CSV does not name its own account,
      currency, or columns, so you supply them inline; a ledger file names all of that itself
      (an entry's amountless posting is the account it came from; with every leg priced, its
      last posting is). Directives, periodic (~) templates, and comments are skipped. The line
      is imported raw and the rules place it, so a ledger file's own categorization is not
      carried in. The extension says which reader a file gets; -format overrides it, so
      hand-kept books in a .txt file import as a ledger without renaming. A registered
      connector is imported by name: it already carries its account
      and currency (from connectors register) and fetches its own lines, through the same
      deduped import every file takes.
      -date-format is a Go layout: the reference date Jan 2, 2006 written the way the column
      writes dates, so MM/DD/YYYY is -date-format 01/02/2006 (the default is 2006-01-02).
  import <connector> [-relogin]
      Import a bank connector's lines. It reuses a saved browser session; when that has
      expired it opens a browser for you to sign in again (your password and 2FA are entered
      there and never stored -- only the resulting session is kept). With no terminal present
      it does not open a browser, it fails with a message to sign in from one. -relogin signs
      in fresh, ignoring any saved session. The account's balance is read at the same time and
      recorded, so reconcile can check the books against the bank. A transfer between two of
      your accounts, seen in both, is paired automatically and booked once (undo it with match).
  import <book.jsonl>
      Merge another book: the log is its own interchange format, so its events replay here in
      their order. Statement lines, invoices, bills, and exports dedupe by fingerprint, so a
      line both books saw lands once. Rules and corrections are recorded again here, later
      than everything this book holds, so where both books answered the same question the
      imported answer wins, and a rule pattern both books authored folds to one rule. A
      transfer each book saw from its own side pairs up once merged. Re-importing the same
      file is a no-op, keyed by a fingerprint of its content.
`},
		{[]string{"categorize"}, `  categorize <fingerprint> (-category <account> | -post <account>=<amount> ... |
             -sell <account>=<qty> ... -gain <account>) [-payee <name>] [-why <reason>] [-actor <name>]
      Assert the postings for one line, overriding the rule for that line only. Use -post
      more than once to split one charge across accounts. A -post amount may name its own
      commodity and an @@ total price, so a share bought with cash is
      -post "Assets:Brokerage:AAPL=10 AAPL @@ 1000.00 USD". A sale instead names the shares
      it disposed of with -sell and where the gain lands with -gain; the cost base, and so the
      gain, is folded from your purchases: -sell "Assets:Brokerage:AAPL=10 AAPL" -gain "Income:Capital Gains".
      -actor records who decided (default human), so a model driving this command is told
      apart from a person in the log; rules set and void take it too.
`},
		{[]string{"void"}, `  void <fingerprint> [-why <reason>] [-actor <name>]
      Annul a bad imported line. The imported fact stays in the log; a later fact supersedes
      it. Voiding an invoice is the same verb on a different noun: invoice void.
`},
		{[]string{"match"}, `  match <fingerprint> (-with <fingerprint> | -break) [-actor <name>]
      Override the automatic transfer fold. On its own the fold pairs the two sightings of one
      movement -- the same amount moving the other way between two of your accounts, within a
      few days -- and books it once, unless a side is already a categorized expense or deposit
      (that is a coincidence, not a transfer). -with forces a pair it missed (say, one dated
      further apart); -break keeps a line it wrongly paired. A later match supersedes.
`},
		{[]string{"export"}, `  export <connector> [-confirm]
      Write rent the books already booked out to a registered connector (see connectors register),
      so its paid/unpaid state stays current. Each rent deposit that a rule attributed to a
      lease (via -meta rentapp.lease=<id>) is recorded against that lease, keyed by the
      deposit's fingerprint so a repeat is a no-op. Without -confirm it is a dry run that
      prints what it would send.
`},
		{[]string{"books"}, `  books [-format table|json|ledger] [-basis cash|accrual] [-since YYYY-MM-DD] [-account <re> ...]
        [-from YYYY-MM-DD] [-to YYYY-MM-DD] [-stdout]
      Fold the log into a table (default), machine-readable JSON, or regenerate
      .bkpr/books.ledger (-stdout writes the ledger to standard output instead).
      -account narrows any of the three to the lines posting to a matching account, at any
      depth, the way ledger matches account names; repeat it to name several accounts, and a
      line posting to any of them is kept. -from and -to narrow by date, both ends inclusive,
      so -from 2026-03-01 -to 2026-03-31 is exactly March and its health line is that month's
      income statement. (-since is different: it chooses how far back the accrual basis books
      invoices and bills at all.) There is no separate review command: the decision queue is
      books -account Uncategorized, each line with the fingerprint to answer it by, and
      -format json is the same queue for an external model, which answers back through
      categorize and rules set. A filtered ledger is a reading and goes to stdout; the artifact
      in the store is only ever the whole books.
      Every format ends with the same health line - income, expenses, net, and money whose kind
      is unknown, one row per commodity - computed once from the same fold, so the formats
      cannot disagree; a filtered reading is summarized as filtered. In the ledger form it is a
      trailing comment, which ledger tools and the import reader both ignore.
      -basis chooses the lens: cash (the default) books only money that moved; accrual also
      books every open invoice and bill, and lets the line that pays one clear its receivable
      or payable. The basis is a read-time choice over one log, so the same books read either
      way and switch with no rewrite. -since sets the effective date of that switch: on -basis
      accrual only invoices and bills dated on or after it are booked, so you can turn on
      accrual mid-year without retroactively accruing everything. An accrual before the date
      reads as cash (its payment books as income or expense when it lands). The caveat is a
      receivable open across the date: it is not shown until it is paid, when it books as cash.
`},
	}},
	{"INVOICES AND BILLS  (value recognized before its cash; only shown on -basis accrual)", []docTopic{
		{[]string{"invoice"}, `  invoice raise -party <name> -amount <amt> -category <Income:...> [-account <a>] [-date <d>] [-currency <c>]
      Raise an invoice: revenue owed to you, earned and billed before the cash moves. It debits
      a receivable and credits income. -account names where it parks, defaulting to
      Assets:Receivable. -date is when the revenue was earned (default today), not when it will
      be paid. The amount is a positive magnitude. Raising the same invoice twice is a no-op,
      keyed by a fingerprint of its content, exactly as re-importing a statement is.
`},
		{[]string{"bill"}, `  bill receive -party <name> -amount <amt> -category <Expenses:...> [-account <a>] [-date <d>] [-currency <c>]
      Receive a bill: money you owe, the mirror of an invoice. It debits an expense and credits
      a payable, defaulting to Liabilities:Payable. Everything else matches invoice raise.
`},
		{[]string{"invoice", "bill"}, `  invoice settle <fingerprint> (-tx <fingerprint> | -reopen)
  bill settle    <fingerprint> (-tx <fingerprint> | -reopen)
      Record that a bank line paid an invoice or bill, so on the accrual basis the cash clears
      the receivable or payable instead of booking the income or expense a second time (that
      was booked when the accrual was raised). A memo does not reliably name which accrual a
      line clears, so this pairing is recorded rather than guessed. -reopen unlinks it; a later
      settle supersedes.
`},
		{[]string{"invoice", "bill"}, `  invoice void <fingerprint> [-why <reason>]
  bill void    <fingerprint> [-why <reason>]
      Drop an accrual that should not have been raised. The same verb as voiding a bad import:
      the raised fact stays in the log; a later fact supersedes it.
`},
		{[]string{"invoice", "bill"}, `  invoice list
  bill list
      List the invoices or bills with their fingerprints, date, party, amount, category, parked
      account, and the line that settled each. An open one also lists CANDIDATES: the bank lines
      that plausibly settle it (same amount, within a few months, not already used elsewhere), so
      settling is picking a fingerprint from a short list rather than grepping the log. The offer
      is never applied on its own, because a memo does not prove which accrual a line clears.
`},
		{[]string{"invoice", "bill"}, `  invoice aging [-as-of <YYYY-MM-DD>]
  bill aging    [-as-of <YYYY-MM-DD>]
      Age the open receivables (invoices) or payables (bills): what is still owed, oldest first,
      each bucketed by how long — current, 31-60, 61-90, 90+ — with a subtotal per bucket. -as-of
      ages against a date other than today. Settled and voided accruals have already left the fold,
      so only what is genuinely outstanding appears.
`},
	}},
	{"POLICIES AND DOCUMENTS  (how a sale folds, and what a transaction prints as)", []docTopic{
		{[]string{"policy"}, `  policy set -method <acb|fifo> [-account <a>] [-actor <name>]
  policy list
      Set the cost-basis method a sale's base is folded under: acb (average cost, the Canadian
      default) or fifo. -account scopes it to one holding account; omitted, it sets the book
      default. The policy is a recorded fact, so changing it re-folds every gain from the log
      rather than restating anything. list shows what stands.
`},
		{[]string{"accounts"}, `  accounts set <account> -meta <k=v> ... [-actor <name>]
      Attach metadata to an account: a letterhead address, a display name, a customer's mailing
      address. A document like an invoice reads it when it renders.
  accounts list
      The account folder: every account you hold -- the bank, card, and loan accounts money is read
      from -- with its display name, what it holds now, and where it stands against the bank. An
      account is yours once a statement imports against it or a connector posts to it.
`},
		{[]string{"reconcile"}, `  reconcile
      Check the books against the bank, to the penny. Every import records the balance the bank
      showed for the account; reconcile folds the books to that date and reports the difference. The
      first balance for an account anchors it (deriving the opening balance the imports do not reach);
      every one after is a real check that no movement since was missed, duplicated, or mispaired. A
      nonzero delta is exactly that gap.
`},
		{[]string{"receipt"}, `  receipt -tx <fingerprint> [-as invoice|receipt] [-format text|html] [-out <file>]
      Render one settled transaction as a printable document: the account's letterhead, the payee
      as the bill-to, the postings as line items, stamped PAID because every line came off a
      statement. It bills in the currency that was billed, so a USD contract paid in CAD reads as
      the USD owed. (This prints money that already moved; invoice raise is for money still owed.)
`},
		{[]string{"report"}, `  report [-income | -balance | -gains] [-basis cash|accrual] [-format text|html] [-account <text>] [-from <D>] [-to <D>] [-out <file>]
      Fold the books into the company's full picture: an income statement over a period, a
      balance sheet as of its end, and the capital-gains schedule a filing wants. Each flag
      narrows to one statement; -basis reads the one log as cash or accrual, chosen at read time
      and stored nowhere.
`},
	}},
}

// docs prints the full command reference, so the CLI is self-documenting.
func docs(w io.Writer) {
	fmt.Fprint(w, docsPreamble)
	for _, g := range reference {
		fmt.Fprintln(w, g.title)
		for i, t := range g.topics {
			if i > 0 {
				fmt.Fprintln(w)
			}
			fmt.Fprint(w, t.text)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprint(w, docsFooter)
}

// helpTopic prints the reference for one command, so finding a flag does not mean scrolling the
// whole of docs.
func helpTopic(w io.Writer, name string) error {
	var found bool
	for _, g := range reference {
		for _, t := range g.topics {
			for _, n := range t.names {
				if n != name {
					continue
				}
				if found {
					fmt.Fprintln(w)
				}
				fmt.Fprint(w, t.text)
				found = true
				break
			}
		}
	}
	if !found {
		return fmt.Errorf(`no help for %q; run "bkpr docs" for the full reference`, name)
	}
	return nil
}

// version is empty in an ordinary build and set by the release build via
// -ldflags "-X main.version=<tag>". When set it names the release; otherwise the
// toolchain's own record (the module version, or (devel) for a plain checkout build)
// is used.
var version string

// versionCmd prints what build this is. It prefers the injected release tag, and
// otherwise reports the info the Go toolchain embeds: the module version when
// installed by tag, or the VCS revision when built from a checkout.
func versionCmd(w io.Writer) {
	mainVersion := version
	var revision, modified string
	if info, ok := debug.ReadBuildInfo(); ok {
		if mainVersion == "" {
			mainVersion = info.Main.Version
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					modified = ", modified"
				}
			}
		}
	}
	if mainVersion == "" {
		mainVersion = "(unknown build)"
	}
	fmt.Fprintln(w, versionLine(mainVersion, revision, modified))
}

// versionLine assembles the display string, abbreviating the revision to a
// git-short length and omitting the parenthetical when there is no revision.
func versionLine(mainVersion, revision, modified string) string {
	line := "bkpr " + mainVersion
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if revision != "" {
		line += fmt.Sprintf(" (%s%s)", revision, modified)
	}
	return line
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

// resetCmd empties the book of record: the log truncated to nothing, the artifact removed, the
// directory still a book. It is the start-over verb, not the correction verb — a wrong fact is
// superseded (void, rules rm, -reopen) and a wrong batch is unwound by git, since every write is
// a pure append. This is the one command that destroys history, so it dry-runs without -confirm.
func resetCmd(args []string) error {
	fs := flag.NewFlagSet("reset", flag.ExitOnError)
	confirm := fs.Bool("confirm", false, "actually empty the book; without it, a dry run")
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	logPath := filepath.Join(s.Path, store.LogFile)
	ledgerPath := s.Ledger()
	events, err := s.Log.All()
	if err != nil {
		s.Close()
		return err
	}

	if len(events) == 0 {
		s.Close()
		fmt.Println("the book is already empty")
		return nil
	}
	if !*confirm {
		s.Close()
		fmt.Printf("would discard %d events and the ledger artifact (dry run; add -confirm to reset)\n", len(events))
		fmt.Printf("after a reset, %s is recoverable only from git\n", logPath)
		return nil
	}

	// The lock is released before the file is touched, so the truncation is not fighting the
	// mirror an open log keeps in memory.
	if err := s.Close(); err != nil {
		return err
	}
	if err := os.Truncate(logPath, 0); err != nil {
		return err
	}
	if err := os.Remove(ledgerPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Printf("reset: %d events discarded; the book is empty\n", len(events))
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

// importCmd reads transactions into the log. Today its argument is a statement file whose details
// are supplied inline (a CSV does not name its own account, currency, or columns). Importing from a
// registered connector by name is the same verb and will land here too; for now a bank statement is
// where the money is read from, since that is the direction built first.
func importCmd(args []string) error {
	arg, rest, err := firstArg(args, "a file to import")
	if err != nil {
		return err
	}
	format, rest, err := peelFormat(rest)
	if err != nil {
		return err
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	named := format != ""
	if !named {
		format = strings.TrimPrefix(strings.ToLower(filepath.Ext(arg)), ".")
	}
	switch format {
	case "csv":
		return importCSV(s.Log, arg, rest)
	case "ledger":
		return importLedger(s.Log, arg)
	case "jsonl":
		return importLog(s.Log, arg)
	}
	if named {
		return fmt.Errorf("import: I do not know the format %q; -format takes csv, ledger, or jsonl", format)
	}
	c, ok, err := books.ConnectorByName(s.Log, arg)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("import: I do not know how to read %q; .csv, .ledger, and .jsonl are supported (-format names one when the extension does not), or a registered connector's name (see `connectors list`)", arg)
	}
	fs := flag.NewFlagSet("import (connector)", flag.ExitOnError)
	relogin := fs.Bool("relogin", false, "ignore any saved sign-in and sign in fresh")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	dir, err := sessionsDir()
	if err != nil {
		return err
	}
	return importConnector(s.Log, c, fetchOpts{
		sessionDir:  dir,
		interactive: interactiveTerminal(),
		relogin:     *relogin,
	})
}

// peelFormat pulls -format out of the flags before dispatch, because which reader parses the rest
// of them depends on its answer. Empty means the file's extension decides, as it always has.
func peelFormat(args []string) (string, []string, error) {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "-format="); ok {
			return v, append(append([]string{}, args[:i]...), args[i+1:]...), nil
		}
		if a == "-format" {
			if i+1 == len(args) {
				return "", nil, fmt.Errorf("-format needs a value: csv, ledger, or jsonl")
			}
			return args[i+1], append(append([]string{}, args[:i]...), args[i+2:]...), nil
		}
	}
	return "", args, nil
}

// sessionsDir is where bank browser sessions are kept: machine-local, keyed by a connector's
// token-env, and deliberately outside the book of record -- a session is a live credential, not
// committed history. Two connectors that share a login share a session because they share a
// token-env.
func sessionsDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "bookkeeper", "sessions"), nil
}

// interactiveTerminal reports whether a person is at the keyboard, so a bank import whose session has
// expired may open a browser to sign in again. Without one -- a pipe, a redirect, an agent, cron --
// it fails fast with a clear message instead of opening a browser nobody is watching.
func interactiveTerminal() bool {
	return isTerminal(os.Stdin)
}

// Source is the input port every import runs: any producer of normalized transactions, a file
// reader or a connector fetch alike.
type Source func() ([]model.Transaction, error)

// importFrom runs a source and records what it yields. It is the one tail every import flows
// through, file or connector, so re-running any of them is a no-op on the lines already in the log.
func importFrom(log *eventlog.Log, label string, src Source) error {
	txs, err := src()
	if err != nil {
		return err
	}
	result, err := books.Import(log, label, txs)
	if err != nil {
		return err
	}
	fmt.Printf("%d entries read: %d imported, %d already in the log\n", len(txs), result.Imported, result.Skipped)
	uncategorizedHint(log)
	return nil
}

// importConnector fetches a registered connector's lines and lands them through the same import
// every file takes, deduped by fingerprint. A bank whose session has expired with no one present to
// sign in again is reported as an actionable hint rather than a raw error.
func importConnector(log *eventlog.Log, c books.Connector, o fetchOpts) error {
	fetch, err := fetcherFor(c.Kind, o)
	if err != nil {
		return err
	}
	label := "connector:" + c.Name
	var got fetchResult
	err = importFrom(log, label, func() ([]model.Transaction, error) {
		r, ferr := fetch(c)
		got = r
		return r.txs, ferr
	})
	if errors.Is(err, source.ErrSessionExpired) {
		return fmt.Errorf("%s: its sign-in has expired; run `bkpr import %s` from a terminal to sign in again", c.Name, c.Name)
	}
	if err != nil {
		return err
	}
	// The scraped balance anchors reconciliation: record what the bank showed, as of now, so the next
	// `reconcile` can check the books against it to the penny.
	if got.hasBalance {
		if err := books.AssertBalance(log, label, c.Account, time.Now(), got.balance); err != nil {
			return err
		}
		fmt.Printf("bank balance recorded: %s reconciles %s\n", got.balance, c.Account)
	}
	return nil
}

// importLog merges another book: the log is its own interchange format, so combining two books is
// an import, not a new serialization. The other book's events replay here in their order; lines,
// invoices, and bills dedupe by fingerprint, recurring facts land later and win, and re-importing
// the same file is a no-op keyed by a fingerprint of its content.
func importLog(log *eventlog.Log, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	events, err := eventlog.ReadEvents(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)

	res, err := books.MergeLog(log, "human", hex.EncodeToString(digest[:])[:16], events)
	if err != nil {
		return err
	}
	if res.Repeat {
		fmt.Printf("%d events read: this file was merged before, nothing recorded\n", len(events))
		return nil
	}
	fmt.Printf("%d events read: %d recorded, %d already known\n", len(events), res.Recorded, res.Skipped)
	uncategorizedHint(log)
	return nil
}

// importLedger reads a plain-text ledger file: each entry's amountless posting is the account its
// statement line came from, and the line's amount is the negation of the priced postings. A ledger
// file already names its own accounts, so the categorization it carries is asserted per line as the
// file wrote it, rather than dropped for the rules to re-derive. An entry the books cannot balance
// (a mixed-commodity placeholder) is left to the rules instead.
func importLedger(log *eventlog.Log, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	txs, entries, err := source.ReadLedger(f)
	if err != nil {
		return err
	}
	actor := "statement:" + filepath.Base(path)
	result, err := books.Import(log, actor, txs)
	if err != nil {
		return err
	}
	carried, skipped, err := books.CarryCategorizations(log, actor, "imported from "+filepath.Base(path), txs, entries)
	if err != nil {
		return err
	}
	fmt.Printf("%d entries read: %d imported, %d already in the log; %d categorized from the file, %d left to the rules\n",
		len(txs), result.Imported, result.Skipped, carried, skipped)
	uncategorizedHint(log)
	return nil
}

// uncategorizedHint says where the rules ran out after an import, so the next step is named rather
// than remembered. It is best-effort: the import it follows has already succeeded, so a fold that
// cannot run only costs the hint.
func uncategorizedHint(log *eventlog.Log) {
	_, entries, err := books.Ledger(log)
	if err != nil {
		return
	}
	var n int
	for _, e := range entries {
		if e.Uncategorized() {
			n++
		}
	}
	if n > 0 {
		fmt.Printf("%d lines in the books are uncategorized; bkpr books -account Uncategorized lists them\n", n)
	}
}

func importCSV(log *eventlog.Log, path string, args []string) error {
	fs := flag.NewFlagSet("import (csv)", flag.ExitOnError)
	var m source.CSV
	fs.StringVar(&m.Account, "account", "", "the ledger account this statement belongs to")
	fs.StringVar(&m.Currency, "currency", "", "the account's currency, e.g. CAD")
	fs.StringVar(&m.Date, "date", "Date", "header of the date column")
	fs.StringVar(&m.Description, "description", "Description", "header of the memo column")
	fs.StringVar(&m.DateFormat, "date-format", "2006-01-02", "Go date layout the column uses, e.g. 01/02/2006 for MM/DD/YYYY")
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
	uncategorizedHint(log)
	return nil
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
	fs.StringVar(&c.Kind, "kind", "rentapp", "which connector this is")
	fs.StringVar(&c.URL, "url", "", "the connector's base URL")
	fs.StringVar(&c.TokenEnv, "token-env", "", "the environment variable holding its bearer token")
	fs.StringVar(&c.Account, "account", "", "the ledger account its transactions land in")
	fs.StringVar(&c.Currency, "currency", "CAD", "the currency of its transactions")
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
	pattern, rest, err := firstArg(args, "the rule's match pattern, e.g. \"acme hardware\"")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("rules set", flag.ExitOnError)
	var r rules.Rule
	var meta metaFlag
	r.Match = pattern
	fs.StringVar(&r.Category, "category", "", "account to post the line to")
	fs.StringVar(&r.Payee, "payee", "", "payee to record on the entry")
	fs.Var(&meta, "meta", "key=value carried onto the entry, repeatable, e.g. rentapp.lease=31")
	before := fs.String("before", "", "on a new rule, place it ahead of the one matching this pattern")
	why := fs.String("why", "", "why the rule changed; changing one reclassifies every line it matched")
	actor := fs.String("actor", "human", "who is authoring this rule; the log records who decided")
	if err := fs.Parse(rest); err != nil {
		return err
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

	was, err := entriesByTx(log)
	if err != nil {
		return err
	}
	if err := upsertRule(log, r, provided, *before, *why, *actor); err != nil {
		return err
	}
	now, err := entriesByTx(log)
	if err != nil {
		return err
	}
	fmt.Printf("rule %q: %d lines reclassified\n", r.Match, reclassified(was, now))
	return nil
}

// entriesByTx folds the books into a comparable rendering per line, keyed by fingerprint, so the
// effect of a rule edit can be counted: fold before, fold after, and the differing lines are the
// reclassification. Editing a rule rewrites history, and the count says how much, at the moment it
// happens — a pattern that catches nothing (or everything) is visible without rendering the books.
func entriesByTx(log *eventlog.Log) (map[string]string, error) {
	txs, entries, err := books.Ledger(log)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(txs))
	for i, tx := range txs {
		out[tx.ID] = entries[i].Payee + "\x00" + accounts(entries[i])
	}
	return out, nil
}

// reclassified counts the lines whose entries differ between two folds of the books, including
// lines the edit added or removed (a changed category can make or break a transfer pairing, which
// drops or restores a line).
func reclassified(was, now map[string]string) int {
	var n int
	for id, before := range was {
		if after, ok := now[id]; !ok || after != before {
			n++
		}
	}
	for id := range now {
		if _, ok := was[id]; !ok {
			n++
		}
	}
	return n
}

// upsertRule adds r, or changes the rule already matching its pattern. On a change only the named
// fields move, and metadata merges per key rather than replacing the bag, so naming one key leaves
// the others. On a new rule the given fields stand and before places it. actor records who decided,
// so a model's rules are told apart from a person's.
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
	match, _, err := firstArg(args, "the rule to remove, by its match pattern")
	if err != nil {
		return err
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	was, err := entriesByTx(log)
	if err != nil {
		return err
	}
	if err := books.RemoveRule(log, "human", match); err != nil {
		return err
	}
	now, err := entriesByTx(log)
	if err != nil {
		return err
	}
	fmt.Printf("removed rule %q: %d lines reclassified\n", match, reclassified(was, now))
	return nil
}

func ruleMove(args []string) error {
	match, rest, err := firstArg(args, "the rule to move, by its match pattern")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("rules mv", flag.ExitOnError)
	before := fs.String("before", "", "move it ahead of this rule; omit to move it to the end")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	was, err := entriesByTx(log)
	if err != nil {
		return err
	}
	if err := books.MoveRule(log, "human", match, *before); err != nil {
		return err
	}
	now, err := entriesByTx(log)
	if err != nil {
		return err
	}
	fmt.Printf("moved rule %q: %d lines reclassified\n", match, reclassified(was, now))
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
	txID, rest, err := firstArg(args, "the transaction fingerprint to categorize")
	if err != nil {
		return err
	}
	var split, sell splitFlag
	fs := flag.NewFlagSet("categorize", flag.ExitOnError)
	category := fs.String("category", "", "post the whole line to this one account")
	payee := fs.String("payee", "", "the payee to record on the entry")
	why := fs.String("why", "", "why this line is categorized so; recorded with the assertion")
	gain := fs.String("gain", "", "on a sale, the account its capital gain or loss lands in, e.g. Income:Capital Gains")
	actor := fs.String("actor", "human", "who is categorizing; the log records who decided")
	fs.Var(&split, "post", "account=amount, repeatable; amount may carry a commodity and an @@ total price, e.g. \"Assets:Brokerage:AAPL=10 AAPL @@ 1000.00 USD\"")
	fs.Var(&sell, "sell", "account=quantity, repeatable; the shares this line sold, e.g. \"Assets:Brokerage:AAPL=10 AAPL\", paired with -gain")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	isSale := len(sell) > 0 || *gain != ""
	switch {
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
		if err := books.Sell(s.Log, *actor, *why, txID, *payee, *gain, disposals); err != nil {
			return err
		}
		fmt.Printf("categorized %s\n", txID)
		return nil
	}

	// The line's own commodity is what a posting is denominated in, so it is fetched before the
	// postings are built and the caller never restates it.
	tx, err := books.Transaction(s.Log, txID)
	if err != nil {
		return err
	}

	post, err := postingsFor(*category, split, tx.Amount)
	if err != nil {
		return err
	}

	if err := books.Categorize(s.Log, *actor, *why, txID, *payee, post); err != nil {
		return err
	}
	// tx.ID rather than the argument: a quoted prefix echoes back as the whole fingerprint.
	fmt.Printf("categorized %s\n", tx.ID)
	return nil
}

// voidCmd annuls a bad imported line, the way to undo a bad import.
func voidCmd(args []string) error {
	txID, rest, err := firstArg(args, "the transaction fingerprint to void")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("void", flag.ExitOnError)
	why := fs.String("why", "", "why the line is annulled; recorded with the void")
	actor := fs.String("actor", "human", "who is voiding; the log records who decided")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	if err := books.VoidTransaction(s.Log, *actor, *why, txID); err != nil {
		return err
	}
	fmt.Printf("voided %s\n", txID)
	return nil
}

// match overrides the automatic transfer fold for one line: force a pairing it missed (mutual naming
// is the only thing it recognises), or break one it wrongly made.
func match(args []string) error {
	txID, rest, err := firstArg(args, "the transaction fingerprint to match")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("match", flag.ExitOnError)
	with := fs.String("with", "", "the other sighting; the two are one movement and the later is dropped")
	brk := fs.Bool("break", false, "this line is not a duplicate; keep it")
	actor := fs.String("actor", "human", "who is matching; the log records who decided")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if *brk == (*with != "") {
		return fmt.Errorf("give -with <tx> to force a pair, or -break to keep a line, not both or neither")
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.Match(log, *actor, txID, *with, !*brk); err != nil {
		return err
	}
	if *brk {
		fmt.Printf("broke the match on %s\n", txID)
	} else {
		fmt.Printf("matched %s with %s\n", txID, *with)
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
	id, rest, err := firstArg(args, "the invoice fingerprint to settle")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("invoice settle", flag.ExitOnError)
	txID := fs.String("tx", "", "the bank line that paid it")
	reopen := fs.Bool("reopen", false, "unlink the invoice from its paying line")
	actor := fs.String("actor", "human", "who is settling; the log records who decided")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if *reopen == (*txID != "") {
		return fmt.Errorf("give -tx <fingerprint> to settle, or -reopen to unlink, not both or neither")
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.SettleInvoice(log, *actor, id, *txID); err != nil {
		return err
	}
	if *reopen {
		fmt.Printf("reopened %s\n", id)
	} else {
		fmt.Printf("settled %s with %s\n", id, *txID)
	}
	return nil
}

// invoiceVoid drops an invoice that should not have been raised.
func invoiceVoid(args []string) error {
	id, rest, err := firstArg(args, "the invoice fingerprint to void")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("invoice void", flag.ExitOnError)
	why := fs.String("why", "", "why it is voided; recorded with the void")
	actor := fs.String("actor", "human", "who is voiding; the log records who decided")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.VoidInvoice(log, *actor, *why, id); err != nil {
		return err
	}
	fmt.Printf("voided %s\n", id)
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
	id, rest, err := firstArg(args, "the bill fingerprint to settle")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("bill settle", flag.ExitOnError)
	txID := fs.String("tx", "", "the bank line that paid it")
	reopen := fs.Bool("reopen", false, "unlink the bill from its paying line")
	actor := fs.String("actor", "human", "who is settling; the log records who decided")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if *reopen == (*txID != "") {
		return fmt.Errorf("give -tx <fingerprint> to settle, or -reopen to unlink, not both or neither")
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.SettleBill(log, *actor, id, *txID); err != nil {
		return err
	}
	if *reopen {
		fmt.Printf("reopened %s\n", id)
	} else {
		fmt.Printf("settled %s with %s\n", id, *txID)
	}
	return nil
}

// billVoid drops a bill that should not have been received.
func billVoid(args []string) error {
	id, rest, err := firstArg(args, "the bill fingerprint to void")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("bill void", flag.ExitOnError)
	why := fs.String("why", "", "why it is voided; recorded with the void")
	actor := fs.String("actor", "human", "who is voiding; the log records who decided")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	if err := books.VoidBill(log, *actor, *why, id); err != nil {
		return err
	}
	fmt.Printf("voided %s\n", id)
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

// accountList is the account folder: every account you hold, its display name if one is set, what it
// holds now, and where it stands against the bank. It folds the owned-account set, the per-account
// balances, and reconciliation into one list, so "what are my accounts" is one command.
func accountList(args []string) error {
	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	owned, err := books.OwnedAccounts(log)
	if err != nil {
		return err
	}
	balances, err := books.Balances(log)
	if err != nil {
		return err
	}
	meta, err := books.AccountMeta(log)
	if err != nil {
		return err
	}
	recs, err := books.Reconcile(log)
	if err != nil {
		return err
	}
	recByAccount := map[string]books.Reconciliation{}
	for _, r := range recs {
		recByAccount[r.Account] = r
	}

	accounts := make([]string, 0, len(owned))
	for a := range owned {
		accounts = append(accounts, a)
	}
	sort.Strings(accounts)

	if len(accounts) == 0 {
		fmt.Println("no accounts yet; import a statement or register a connector")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ACCOUNT\tNAME\tBALANCE\tRECONCILED")
	for _, a := range accounts {
		name := meta[a]["name"]
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a, name, balanceCell(balances[a]), reconciledCell(recByAccount[a]))
	}
	return w.Flush()
}

// balanceCell renders an account's holdings, one amount per commodity (a USD fee beside CAD rent do
// not sum), or a dash when it holds nothing yet.
func balanceCell(per map[string]model.Amount) string {
	if len(per) == 0 {
		return "-"
	}
	commodities := make([]string, 0, len(per))
	for c := range per {
		commodities = append(commodities, c)
	}
	sort.Strings(commodities)
	parts := make([]string, 0, len(commodities))
	for _, c := range commodities {
		parts = append(parts, per[c].String())
	}
	return strings.Join(parts, ", ")
}

// reconciledCell says where an account stands against the bank: matched to the penny as of a date, off
// by a stated amount, or unchecked when no balance has been scraped yet.
func reconciledCell(r books.Reconciliation) string {
	if r.Account == "" {
		return "-" // no bank balance recorded for this account yet
	}
	if r.Reconciled {
		return "yes, as of " + r.AsOf.Format("2006-01-02")
	}
	return fmt.Sprintf("off by %s (as of %s)", r.Delta, r.AsOf.Format("2006-01-02"))
}

// reconcileCmd shows every account with a scraped balance against the books: what the bank last said
// it held, what the books fold to on that date, and the difference. It writes nothing; it is a fold.
func reconcileCmd(args []string) error {
	log, closeLog, err := open()
	if err != nil {
		return err
	}
	defer closeLog()

	recs, err := books.Reconcile(log)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		fmt.Println("no bank balances recorded yet; import a bank connector to record one")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ACCOUNT\tAS OF\tBANK\tBOOKS\tDELTA")
	allReconciled := true
	for _, r := range recs {
		status := r.Delta.String()
		if r.Reconciled {
			status = "0 (reconciled)"
		} else {
			allReconciled = false
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Account, r.AsOf.Format("2006-01-02"), r.Bank, r.Books, status)
	}
	w.Flush()
	if allReconciled {
		fmt.Println("\nall accounts reconcile to the penny")
	}
	return nil
}
