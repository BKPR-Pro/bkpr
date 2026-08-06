// Command bkpr turns bank and card statements into a set of books.
//
// A directory holds a set of books the way it holds a git repository, marked by `.bkpr` and
// found by walking up. `import` records what a statement said, once per line, into the append-only
// log inside it. `books` folds that log back out through a rule set and renders it. Where the
// rules run out of knowledge the account path stops at Uncategorized rather than guessing, and the
// agent operating the tool answers those through the same commands a person would. bkpr is
// built to be driven by an agent; it holds no model of its own.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	"github.com/BKPR-Pro/bkpr/lib/adapters/source"
	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/eventlog"
	"github.com/BKPR-Pro/bkpr/lib/model"
	"github.com/BKPR-Pro/bkpr/lib/rules"
	"github.com/BKPR-Pro/bkpr/lib/store"
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
	case "unvoid":
		err = unvoidCmd(os.Args[2:])
	case "comment":
		err = commentCmd(os.Args[2:])
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
	case "balance":
		err = balanceCmd(os.Args[2:])
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
	case "register":
		err = registerCmd(os.Args[2:])
	case "categorize-ui":
		err = categorizeUI(os.Args[2:])
	case "completion":
		err = completionCmd(os.Args[2:])
	case "__complete":
		runComplete(os.Stdout, os.Args[2:], liveConnectors)
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
		fmt.Fprintf(os.Stderr, "bkpr: %v\n", err)
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
		{"connectors register", "<name> -kind <rentapp|rbc|simplii|pcfinancial> -url <url> -token-env <ENV> -account <a> [-currency <c>] [-cred field=ref ...] [-secret-cmd <cmd>] [-account-path <label> ...] [-history <days>]"},
		{"connectors rm", "<name>"},
		{"connectors list", ""},
	}},
	{"RULES", []usageLine{
		{"rules set", "<re> [-amount <amt>] [-category <account>] [-payee <name>] [-source <account>] [-tax-rate <pct> -tax-account <account> [-tax-category <re>] [-tax-from <date>]] [-meta <k=v> ...] [-before <re>] [-why <reason>] [-actor <name>]"},
		{"rules rm", "<re> [-amount <amt>]"},
		{"rules mv", "<re> [-amount <amt>] [-before <re>]"},
		{"rules list", ""},
	}},
	{"BOOKKEEPING", []usageLine{
		{"import", "<file.csv> -account <a> -currency <c> (-amount <col> | -debit <col> -credit <col>) [-date <col> -description <col> -date-format <layout>]"},
		{"import", "<file.ledger>"},
		{"import", "<book.jsonl>"},
		{"import", "<file> -format csv|ledger|jsonl"},
		{"import", "<connector> [-relogin] [-history <days> | -from <date> [-to <date>]]"},
		{"import", "-all [-relogin] [-history <days> | -from <date> [-to <date>]]"},
		{"categorize", "<fingerprint> (-category <account> [-tax-rate <pct> -tax-account <account>] | -post <account>=<amount> ...) [-payee <name>] [-invoice <n|next>] [-source <account>] [-why <reason>] [-actor <name>]"},
		{"categorize-ui", "[-from <YYYY-MM-DD>] [-to <YYYY-MM-DD>] [-out <file>]"},
		{"categorize-ui apply", "<file.json> [-actor <name>] [-why <reason>]"},
		{"comment", "<fingerprint> (-text <note> | -remove) [-account <a>] [-why <reason>] [-actor <name>]"},
		{"void", "<fingerprint> [-why <reason>] [-actor <name>]"},
		{"unvoid", "<fingerprint> [-why <reason>] [-actor <name>]"},
		{"match", "<fingerprint> (-with <fingerprint> | -break) [-actor <name>]"},
		{"export", "<connector> [-confirm]"},
		{"books", "[-format table|json|ledger] [-basis cash|accrual] [-since <YYYY-MM-DD>] [-account <re> ...] [-from <YYYY-MM-DD>] [-to <YYYY-MM-DD>] [-sort amount [-desc]] [-value <c> [-rate <C=n> ...]] [-stdout]"},
		{"register", "[-account <re>] [-basis cash|accrual] [-since <YYYY-MM-DD>] [-from <YYYY-MM-DD>] [-to <YYYY-MM-DD>] [-dups]"},
	}},
	{"INVOICES AND BILLS", []usageLine{
		{"invoice raise", "-party <name> -amount <amt> -category <account> [-account <a>] [-date <YYYY-MM-DD>] [-currency <c>] [-invoice <n|next>] [-description <text>] [-tax-rate <pct> -tax-account <account>] [-why <reason>] [-actor <name>]"},
		{"invoice settle", "<fingerprint> (-tx <fingerprint> | -reopen) [-actor <name>]"},
		{"invoice void", "<fingerprint> [-why <reason>] [-actor <name>]"},
		{"invoice list", ""},
		{"invoice aging", "[-as-of <YYYY-MM-DD>]"},
		{"invoice next", ""},
		{"bill receive", "-party <name> -amount <amt> -category <account> [-account <a>] [-date <YYYY-MM-DD>] [-currency <c>] [-invoice <n>] [-description <text>] [-tax-rate <pct> -tax-account <account>] [-why <reason>] [-actor <name>]"},
		{"bill settle", "<fingerprint> (-tx <fingerprint> | -reopen) [-actor <name>]"},
		{"bill void", "<fingerprint> [-why <reason>] [-actor <name>]"},
		{"bill list", ""},
		{"bill aging", "[-as-of <YYYY-MM-DD>]"},
	}},
	{"POLICIES AND DOCUMENTS", []usageLine{
		{"policy set", "-method <acb|fifo> [-account <a>] [-actor <name>]"},
		{"policy list", ""},
		{"accounts set", "<account> -meta <k=v> ... [-actor <name>]"},
		{"accounts list", "[-sort amount [-desc]]"},
		{"accounts due", "[-format table|json]"},
		{"balance set", "<account> <amount> [-as-of <YYYY-MM-DD>] [-actor <name>]"},
		{"balance rm", "<account> [-actor <name>]"},
		{"reconcile", ""},
		{"receipt", "-tx <fingerprint> [-as invoice|receipt] [-format text|html|json] [-out <file>]"},
		{"report", "[-basis cash|accrual] [-format text|html|json] [-account <text>] [-from <D>] [-to <D>] [-out <file>]"},
		{"report income", "[-basis cash|accrual] [-format text|html|json] [-account <text>] [-from <D>] [-to <D>] [-out <file>]"},
		{"report balance", "[-basis cash|accrual] [-format text|html|json] [-account <text>] [-from <D>] [-to <D>] [-out <file>]"},
		{"report gains", "[-basis cash|accrual] [-format text|html|json] [-account <text>] [-from <D>] [-to <D>] [-out <file>]"},
	}},
	{"MORE", []usageLine{
		{"completion", "<bash|zsh|fish>"},
		{"help", "[command]"},
		{"docs", ""},
		{"version", ""},
	}},
}

const usageIntro = `Every command finds the nearest .bkpr directory by walking up from where you are.
Run "bkpr help <command>" for one command, "bkpr docs" for the full reference.
The thing a command acts on is its first argument; flags assert facts about it.
Wherever a fingerprint is taken, a unique prefix of it is enough.
-format defaults to the human form (table or text) at a terminal, json off one (a pipe,
a redirect, a script, an agent) -- no flag needed either way; name -format to override.
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

// wordmark is the ASCII banner spelling BKPR. Caps keep a flat baseline, so the rows line up with no
// descenders to fight. Each row is a separate quoted string so its trailing spaces sit before the
// closing quote and survive an editor's whitespace trim; every row is one width, pinned by a test.
var wordmark = strings.Join([]string{
	" ____   _  __ ____   ____  ",
	"| __ ) | |/ /|  _ \\ |  _ \\ ",
	"|  _ \\ | ' / | |_) || |_) |",
	"| |_) || . \\ |  __/ |  _ < ",
	"|____/ |_|\\_\\|_|    |_| \\_\\",
}, "\n")

// masthead is the banner atop the usage screen: the ASCII wordmark painted in the heading style over
// a dimmed tagline, so the help opens on something composed rather than a bare sentence. When the
// palette is off the styles are empty, so a pipe gets the same banner in clean text.
func masthead(p palette) string {
	var b strings.Builder
	for _, line := range strings.Split(wordmark, "\n") {
		fmt.Fprintf(&b, "%s%s%s\n", p.heading, line, p.reset)
	}
	fmt.Fprintf(&b, "\n  %sbkpr · books that balance themselves%s\n", p.dim, p.reset)
	return b.String()
}

// writeUsage renders the grouped command list to w in the given palette: a boxed masthead, one
// section header per group, and every verb aligned into a column so its arguments line up.
func writeUsage(w io.Writer, p palette) {
	fmt.Fprint(w, masthead(p))
	fmt.Fprintln(w)
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

const docsPreamble = `bkpr (bkpr) - turn bank and card statements into a plain-text double-entry ledger.

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
		{[]string{"connectors"}, `  connectors register <name> -kind <kind> -url <url> -token-env <ENV> -account <a> [-currency <c>] [-cred field=ref ...] [-secret-cmd <cmd>]
      Register a live connector. Kinds: rentapp (export), and the banks imported from --
      rbc, simplii, pcfinancial. No secret is stored: -token-env names where the credential
      lives, read when the connector is used. For a bank that credential is a saved browser
      session rather than a token, so connectors that share a login share a -token-env and
      thus one sign-in (every RBC account, say). To sign in unattended, -cred names where each
      login field lives -- a reference, not the secret, e.g. -cred username=op://Private/RBC/username
      -cred password=op://Private/RBC/password -- and -secret-cmd is the command that resolves a
      reference ({} is the reference; default "op read {}", so 1Password works out of the box, and
      any store with a CLI can be named instead). References are safe to commit; the secret is
      fetched only at sign-in and never written down. When an account has no stable URL and is reached
      by menu (RBC), -account-path names each link to click after sign-in, in order, e.g.
      -account-path "Go to RBC Business Banking" -account-path "Current Account"; several accounts
      then share one login and one -url, differing only by their path. A connector is bidirectional in
      principle: export writes to it, import reads from it. Registering one does not itself move data.
  connectors rm <name>            Forget a connector.
  connectors list                 Show the registered connectors.
`},
		{[]string{"completion"}, `  completion <bash|zsh|fish>
      Print a shell completion script. Source it so the shell completes bkpr's commands, their
      subcommands and flags, and -- read live from the book you are in -- your connector names.
      Add to the shell's startup file, after its completion system is initialized:
        bash:  eval "$(bkpr completion bash)"    (in ~/.bashrc)
        zsh:   eval "$(bkpr completion zsh)"     (in ~/.zshrc, after compinit)
        fish:  bkpr completion fish | source     (in ~/.config/fish/config.fish)
      The shell calls bkpr back as you type; nothing is stored and no book is opened unless a
      connector name is what is being completed.
`},
	}},
	{"RULES  (deterministic categorization; first matching rule wins per field)", []docTopic{
		{[]string{"rules"}, `  rules set <re> [-amount <amt>] [-category <account>] [-payee <name>] [-source <account>]
            [-tax-rate <pct> -tax-account <account> [-tax-category <re>] [-tax-from <date>]]
            [-meta <k=v> ...] [-before <re>] [-why <reason>]
      Add a rule, or change one already matching this pattern. On an existing rule only the
      fields you name change, and since that reclassifies every past line it matched, it
      takes a -why. A new rule lands last unless -before places it ahead of another. Account
      paths are free-form and may stop at Uncategorized wherever knowledge runs out. -amount
      narrows a rule to lines of one magnitude, so several payees that share a memo but differ
      only by amount become one rule each on the pattern (property tax $175 to one house, $155
      to another); a bare amount is read as CAD, "175 USD" names another, and the sign is
      ignored so a charge and its refund both match. A pattern and its amount together are a
      rule's identity, so the same pattern takes as many amount-qualified rules as it has
      amounts. -tax-rate and -tax-account (required together) mark a vendor whose charge
      already includes sales tax: the rate is extracted from the total (net = total / (1 +
      rate)) onto the category, the tax onto its account, e.g. -tax-rate 15% -tax-account
      "Assets:HST ITC". -tax-category scopes that tax to the categories matching its pattern,
      because the right treatment can depend on the category rather than the vendor (the same
      hardware store sells to a property whose tax is claimable and to one whose is not); a
      category outside the scope stays gross, and the pattern's capture groups may appear in
      -tax-account ($1), so one rule derives each property's tax account from the category the
      line took. -tax-from bounds the tax by date: a line dated before it stays gross, so a
      filed year whose lines already carry their splits cannot re-split. Setting -tax-from also
      opts the rule's tax into overlaying lines something else categorized: the vendor's tax is
      a fact about the vendor and the category a fact about the line, so a matching line
      categorized by hand (or by another rule) still splits, from that date on -- the human
      keeps the category decision, the rule keeps the arithmetic. The overlay touches only a
      single-leg entry whose leg the tool derived: postings spelled with -post, a hand-made
      split, a carried ledger entry, and a leg already on the tax account all stand as written.
      Without -tax-from a taxed rule behaves as it always has, splitting only the lines it
      categorizes itself. -source routes the matched line's card/liability leg to a sub-account
      instead of the account it was imported on, so a physical card registered on one parent
      splits by purpose: each charge self-routes to its purpose child, and the parent reconciles
      to the one bank balance by rolling those children up. -meta attaches opaque key=value pairs
      (repeatable) that a connector reads by name, e.g. -meta rentapp.lease=31 tells the export
      which lease a matching rent deposit belongs to. Either way it reports how many lines the
      books reclassified, so a pattern that catches nothing (or too much) is visible at once.
  rules rm  <re> [-amount <amt>]              Remove a rule (-amount picks the variant).
  rules mv  <re> [-amount <amt>] [-before <re>]   Reorder a rule (-before omitted moves it last).
  rules list                                  Show the rules in order.
`},
	}},
	{"BOOKKEEPING", []docTopic{
		{[]string{"import"}, `  import <file.csv> -account <a> -currency <c> (-amount <col> | -debit <col> -credit <col>)
                    [-date <col>] [-description <col>] [-date-format <layout>]
  import <file.ledger>
  import <file> -format csv|ledger|jsonl
      Read transactions in. A file is a one-time input: a CSV does not name its own account,
      currency, or columns, so you supply them inline and its lines are imported raw for the
      rules to place; a ledger file names all of that itself (an entry's amountless posting is
      the account it came from; with every leg priced, its last posting is) and already spells
      out its categorization, which is carried in per line rather than re-derived (a line the
      books cannot balance is left to the rules). An account directive's address lines are read
      into that account's metadata, the same the letterhead uses; periodic (~) templates and
      comments are skipped. The extension says which reader a file gets; -format overrides it, so
      hand-kept books in a .txt file import as a ledger without renaming. A registered
      connector is imported by name: it already carries its account
      and currency (from connectors register) and fetches its own lines, through the same
      deduped import every file takes.
      -date-format is a Go layout: the reference date Jan 2, 2006 written the way the column
      writes dates, so MM/DD/YYYY is -date-format 01/02/2006 (the default is 2006-01-02).
  import <connector> [-relogin] [-history <days> | -from <date> [-to <date>]]
      Import a bank connector's lines. It reuses a saved browser session; when that has
      expired it opens a browser for you to sign in again (your password and 2FA are entered
      there and never stored -- only the resulting session is kept). With no terminal present
      it does not open a browser, it fails with a message to sign in from one. -relogin signs
      in fresh, ignoring any saved session. By default it reads the account's short recent
      window; -history <days> reads that many days back (a relative window, good for a routine
      pull), and -from/-to read an explicit range for backfilling a known period (-to defaults
      to today, and -from overrides -history). Dates are written as 2026-02-01 or "Feb 1, 2026".
      Imports dedupe by fingerprint, so a wider window never duplicates. The account's balance is
      read at the same time and recorded, so reconcile can check the books against the bank. A
      transfer between two of your accounts, seen in both, is paired automatically and booked
      once (undo it with match).
  import -all [-relogin] [-history <days> | -from <date> [-to <date>]]
      Import every registered connector that can be read (a rentapp-style export-only connector
      is named and skipped). Connectors that share a login are run back to back so one sign-in
      covers the group -- every RBC account behind one browser session, both Simplii accounts
      behind another -- and -relogin refreshes each login once, not once per account. It keeps
      going when a connector fails (an expired session, a page that changed), reports which ones
      did, and exits non-zero if any failed. The -history and -from/-to windows apply to them
      all, and every line lands through the same deduped import a single connector takes.
  import <book.jsonl>
      Merge another book: the log is its own interchange format, so its events replay here in
      their order. Statement lines, invoices, bills, and exports dedupe by fingerprint, so a
      line both books saw lands once. Rules and corrections are recorded again here, later
      than everything this book holds, so where both books answered the same question the
      imported answer wins, and a rule pattern both books authored folds to one rule. A
      transfer each book saw from its own side pairs up once merged. Re-importing the same
      file is a no-op, keyed by a fingerprint of its content.
`},
		{[]string{"categorize"}, `  categorize <fingerprint> (-category <account> [-tax-rate <pct> -tax-account <account>] |
             -post <account>=<amount> ... |
             -sell <account>=<qty> ... -gain <account>) [-payee <name>] [-invoice <n>] [-source <account>] [-why <reason>] [-actor <name>]
      Assert the postings for one line, overriding the rule for that line only. Use -post
      more than once to split one charge across accounts. A -post amount may name its own
      commodity and an @@ total price, so a share bought with cash is
      -post "Assets:Brokerage:AAPL=10 AAPL @@ 1000.00 USD". With -category, -tax-rate and
      -tax-account (required together) split the tax the total already includes, exactly as a
      taxed rule does: the pre-tax amount (net = total / (1 + rate)) to the category, the exact
      remainder to the tax account, so the hand form of the decision cannot drift a cent from
      the rule form. The two forms rank differently against a taxed rule's overlay (rules set
      -tax-from): a -category assertion stays open to it, because the vendor's tax is a fact the
      category decision does not answer, while -post legs are your own arithmetic and stand as
      written. A sale instead names the shares
      it disposed of with -sell and where the gain lands with -gain; the cost base, and so the
      gain, is folded from your purchases: -sell "Assets:Brokerage:AAPL=10 AAPL" -gain "Income:Capital Gains".
      -invoice records the invoice or bill number for the line, the ledger (code); it renders as
      "(2073)" before the payee and reads back into its own field. -invoice next takes the number
      after the highest the book has issued, so tagging a line with an invoice of your own does not
      depend on remembering where the sequence got to; a number you were given -- a vendor's -- is
      recorded exactly as you type it.
      -source routes this line's card/liability leg to a sub-account instead of the account it was
      imported on, e.g. -source "Liabilities:PC Mastercard:9 Birch Street"; the parent it rolls up to
      still reconciles to the one bank balance. It is the per-line form of a rules -source route.
      -actor records who decided (default human), so a model driving this command is told
      apart from a person in the log; rules set and void take it too.
`},
		{[]string{"categorize-ui"}, `  categorize-ui [-from <YYYY-MM-DD>] [-to <YYYY-MM-DD>] [-out <file>]
      Render a self-contained HTML page listing every line in the window (default: the four
      months up to -to, or today), its payee, amount, and current category next to it in a text
      box with autocomplete drawn from every account a posting has ever landed on. Open it in a
      browser, edit whatever categories are wrong, and its Export button downloads a JSON file
      of just the changed lines -- fingerprint, old and new category -- for categorize-ui apply
      to read back, or for a person to hand to an agent. Nothing here writes to the books; the
      page and its export are both read-only until applied. -out writes the page to a file
      instead of stdout.
  categorize-ui apply <file.json> [-actor <name>] [-why <reason>]
      Read an export back in and assert each line's new category, the same as running
      categorize <fingerprint> -category <account> by hand for every row. A line whose export
      carries a tax split is skipped with a warning: the export names only the one category a
      plain assertion would replace both postings with, so applying it would silently drop the
      tax leg; recategorize that line with categorize -tax-rate or -post instead. -actor records
      who decided (default human); -why is recorded with each assertion (default "categorize-ui").
`},
		{[]string{"comment"}, `  comment <fingerprint> (-text <note> | -remove) [-account <a>] [-why <reason>] [-actor <name>]
      Leave a free-text note on one posting of a line, or clear it with -remove. The note is
      commentary the books carry beside the account and amount: it renders inline in the ledger
      artifact (Expenses:Repairs  84.20 CAD  ; the note) and reads back, so a reason left on a
      split is durable. -account names which leg the note belongs to and is needed only when the
      line splits across several; a line with one posting needs no -account. Naming the line's own
      account notes the source (elided) leg -- the balancing posting the ledger infers -- so a
      reason for where the money came from is reachable too. Commenting a line the rules categorized
      freezes their answer for that one line, the way a correction does.
`},
		{[]string{"void"}, `  void <fingerprint> [-why <reason>] [-actor <name>]
      Annul a bad imported line. The imported fact stays in the log; a later fact supersedes
      it. Voiding an invoice is the same verb on a different noun: invoice void. A void made
      by mistake is reversed with unvoid.
`},
		{[]string{"unvoid"}, `  unvoid <fingerprint> [-why <reason>] [-actor <name>]
      Reverse a void made by mistake. Restores the line to the books and to categorizing, as
      though it had never been voided. Refused if the fingerprint is not currently voided.
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
      deposit's fingerprint so a repeat is a no-op. When the deposit settles a numbered invoice,
      its number rides along as the reference the app cites in the payment's description.
      Without -confirm it is a dry run that prints what it would send.
      A period can be marked paid from either side: a human recording it in the app's own UI, or
      this export recording it here. When the app already has a period recorded, the export
      reports it as already recorded rather than an error -- the other valid path simply got
      there first.
`},
		{[]string{"books"}, `  books [-format table|json|ledger] [-basis cash|accrual] [-since YYYY-MM-DD] [-account <re> ...]
        [-from YYYY-MM-DD] [-to YYYY-MM-DD] [-sort amount [-desc]] [-value <c> [-rate <C=n> ...]] [-stdout]
      Fold the log into a table, machine-readable JSON, or regenerate .bkpr/books.ledger
      (-stdout writes the ledger to standard output instead). -format defaults to table at a
      terminal and json off one, so a person and a script reading the same command get the
      form each wants with no flag; -format ledger is never a default, only ever named.
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
      Lines print by date by default; -sort amount orders them by amount instead (-desc for
      largest first), a display choice over the same fold, so the health line never changes.
      It does not apply to -format ledger, which stays in its canonical date order.
      -basis chooses the lens: cash (the default) books only money that moved; accrual also
      books every open invoice and bill, and lets the line that pays one clear its receivable
      or payable. The basis is a read-time choice over one log, so the same books read either
      way and switch with no rewrite. -since sets the effective date of that switch: on -basis
      accrual only invoices and bills dated on or after it are booked, so you can turn on
      accrual mid-year without retroactively accruing everything. An accrual before the date
      reads as cash (its payment books as income or expense when it lands). The caveat is a
      receivable open across the date: it is not shown until it is paid, when it books as cash.
      -value reads a mixed-commodity book in one currency: every amount, each line's and the
      health line's alike, is restated into it - a posting at the @@ price it recorded (USD income
      booked at the CAD it was worth reads as that CAD), a source line as the negation of its
      postings when they reach the target. A line with no recorded price is valued at a -rate you
      supply (-rate USD=1.35, one unit's worth in the target, comma-separated for several), and one
      with neither is left in its own currency and named in a warning. Valuing restates a reading,
      not the artifact, so -value cannot render -format ledger, which stays each line's own commodity.
`},
		{[]string{"register"}, `  register [-account <re>] [-basis cash|accrual] [-since <YYYY-MM-DD>] [-from <YYYY-MM-DD>] [-to <YYYY-MM-DD>] [-dups]
      Read the books the way the bank prints a statement: every line in date order -- payee,
      amount, the account it moved, and the door it entered through (the connector or file that
      imported it, which no other view shows). It reads through the same lens books does: -basis
      and -since fold the very lines that make books' totals, so on -basis accrual the invoices
      and bills appear too, each with its kind as its door. A transfer the fold paired appears
      once, and each account still sees its own side of it; voided lines have left the fold.
      -account narrows to one account's statement and adds a balance running down the page,
      counting the source leg the fold books there and any posting that lands there, so a card
      whose charges route to purpose sub-accounts reads rolled up under the parent pattern.
      -from and -to narrow by date, both ends inclusive. It is a pure reading and writes nothing.
      -dups reads the same fold for candidate twins: lines sharing a date and an amount that
      entered through different doors. That is the double the books cannot catch on their own --
      fingerprints dedupe within a door, and reconcile compares each door's lines to its own
      bank -- so one purchase entering through two doors (a hand ledger and a connector, say)
      doubles silently, and on -basis accrual an open invoice collides with a deposit that
      booked the same income directly. One door colliding with itself is not reported: a bank
      legitimately charges the same amount twice in a day. A settled accrual never twins with
      the line that paid it -- that pairing is recorded, not a coincidence. Each group prints the
      shared date and amount, then every line with its fingerprint, door, account, and
      categorization; the report only ever suggests, and a twin that is real is voided on one
      side by hand.
`},
	}},
	{"INVOICES AND BILLS  (value recognized before its cash; only shown on -basis accrual)", []docTopic{
		{[]string{"invoice"}, `  invoice raise -party <name> -amount <amt> -category <Income:...> [-account <a>] [-date <d>] [-currency <c>] [-invoice <n>]
                [-description <text>] [-tax-rate <pct> -tax-account <account>]
      Raise an invoice: revenue owed to you, earned and billed before the cash moves. It debits
      a receivable and credits income. -account names where it parks, defaulting to
      Assets:Receivable. -date is when the revenue was earned (default today), not when it will
      be paid. The amount is a positive magnitude. -invoice records the invoice number; it renders
      as the ledger (code) -- "(2073)" before the party -- and is metadata, not part of the
      fingerprint, so numbering an invoice never changes its identity. Raising the same invoice
      twice is a no-op, keyed by a fingerprint of its content, exactly as re-importing a statement is;
      because the number is not in the fingerprint, a repeat raise keeps the number it was first
      given and spends no new one. -invoice next issues the number after the highest the book holds,
      so the sequence is read from the books rather than remembered -- and skipped numbers, the cost
      of remembering, stop happening.
      -tax-rate and -tax-account (required together) say the amount already includes sales tax you
      collected: the receivable stays the whole sum owed, while the income side splits into the net
      and the tax, e.g. -amount 1150.00 -tax-rate 15% -tax-account Liabilities:HST books 1,000 of
      income and 150 of HST owed. Same words, and the same tax-inclusive split, as categorize.
      -description says what is being billed, e.g. "Rent for Aug 1", and becomes the label of the
      income line on the document receipt renders. It describes the invoice rather than identifying
      it, so it stays out of the fingerprint: re-describing an invoice does not make it a new one.
      The tax line is labelled by its own account -- give that account a name with
      accounts set <account> -meta name=HST, and every document reads HST there.
`},
		{[]string{"bill"}, `  bill receive -party <name> -amount <amt> -category <Expenses:...> [-account <a>] [-date <d>] [-currency <c>] [-invoice <n>]
               [-description <text>] [-tax-rate <pct> -tax-account <account>]
      Receive a bill: money you owe, the mirror of an invoice. It debits an expense and credits
      a payable, defaulting to Liabilities:Payable. -invoice records the bill number (the vendor's
      invoice number), rendered as the ledger (code). -tax-rate and -tax-account split the tax you
      paid out of the expense, so it lands where it is claimed back from rather than inflating the
      cost. There is no "next" here: the number on a bill was issued by the vendor, so it is only
      ever copied off their document. Everything else matches invoice raise.
`},
		{[]string{"invoice", "bill"}, `  invoice settle <fingerprint> (-tx <fingerprint> | -reopen)
  bill settle    <fingerprint> (-tx <fingerprint> | -reopen)
      Record that a bank line paid an invoice or bill, so on the accrual basis the cash clears
      the receivable or payable instead of booking the income or expense a second time (that
      was booked when the accrual was raised). When the cash is in a different currency than the
      accrual -- a CAD deposit paying a USD invoice -- it clears the whole parked amount in its own
      commodity, priced at the cash that actually landed (@@), so a foreign receivable nets to zero
      rather than being left holding two currencies. A memo does not reliably name which accrual a
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
		{[]string{"invoice"}, `  invoice next
      Print the number the next invoice would take, and nothing else, so it can be read by a person
      writing a document by hand or by a script. It is one past the highest number the book has
      issued -- across raised invoices and the lines categorize tagged, which are the one sequence --
      counting only numbers that are entirely digits, since a vendor's own numbering is not ours to
      continue. It never fills a gap: a missing number may already be printed on a document that was
      withheld or voided, so reissuing it would put two invoices on one number. -invoice next on
      invoice raise and categorize resolves through this, so the number is read from the books rather
      than held in your head.
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
      address. A document like an invoice reads it when it renders. Importing a ledger file
      records the same metadata from its account directives, and regenerating the books writes
      those directives back at the head of the file, so a hand-kept letterhead round-trips.
  accounts list [-sort amount [-desc]]
      The account folder: every account you hold -- the bank, card, and loan accounts money is read
      from -- with its display name, what it holds now, and where it stands against the bank. An
      account is yours once a statement imports against it or a connector posts to it. Listed by name
      by default; -sort amount orders by balance instead (-desc for largest first). An account holding
      more than one commodity sorts by the sum of its balances.
  accounts due [-format table|json]
      Every Liabilities: account family with a nonzero net balance -- credit cards, lines of credit --
      alongside the due date and minimum payment recorded on it (accounts set <account> -meta
      due=<YYYY-MM-DD> minimum=<amount>). A family is the bare account plus every purpose-split
      :child account filed under it; their balances net into one row, read from and set on the bare
      parent, so a split that leaves one bucket looking positive does not hide what the card overall
      owes. A family owing money with neither key set still appears, with blank columns, as a nudge
      to fill them in. Sorted soonest-due first; a family with no due date sorts last. -format
      defaults to table at a terminal and json off one.
`},
		{[]string{"balance"}, `  balance rm <account> [-actor <name>]
      Stop reconciling an account: it is finished, so leave it out of the report. Nothing is
      deleted -- its transactions and recorded balances stay in the log, and recording a balance
      afterwards starts the account over from there. This is for an account that was emptied
      rather than one that was wrong (a connector re-pointed away from it, its lines collapsed
      onto the account they belonged to). Asserting 0.00 does not do this: the first balance an
      account carries derives its opening figure, so it matches by construction however little it
      holds, and a later zero reads as a delta rather than as a close.

  balance set <account> <amount> [-as-of <YYYY-MM-DD>] [-actor <name>]
      Record what an account held on a date, by hand -- the anchor reconcile checks against, for an
      account no connector reports (one imported from CSV or ledger). A connector records this on
      every import; this is the same fact entered by hand. The amount carries its commodity, e.g.
      "100.00 CAD". A liability is entered as the statement shows it, a positive amount owing, and
      stored negative, so a hand-set anchor signs the same way a scraped one does. Without -as-of the
      balance is dated today. The first balance for an account anchors it; see reconcile.
`},
		{[]string{"reconcile"}, `  reconcile [-history <account>]
      Check the books against the bank, to the penny. Every import records the balance the bank
      showed for the account; reconcile folds the books to that date and reports the difference. The
      first balance for an account anchors it (deriving the opening balance the imports do not reach);
      every one after is a real check that no movement since was missed, duplicated, or mispaired. A
      nonzero delta is exactly that gap.

      -history <account> reruns that same check at every bank-asserted balance on record for one
      account, in date order, instead of just the latest -- so a delta that only shows up in the
      current check can be bisected to the specific assertion (the specific import) that first
      introduced it.
`},
		{[]string{"receipt"}, `  receipt -tx <fingerprint> [-as invoice|receipt] [-format text|html|json] [-out <file>]
      Render one transaction as a printable document: the account's letterhead, the payee as the
      bill-to, the postings as line items. It bills in the currency that was billed, so a USD
      contract paid in CAD reads as the USD owed. -tx takes a bank line's fingerprint or a raised
      invoice's or bill's, the bare one invoice list prints, so the document you send exists before
      its cash does. PAID is read rather than assumed: a line off a statement has cleared, and an
      accrual is stamped only once a settle links the bank line that paid it. Where the accrual
      carries an invoice number, that number heads the document instead of the fingerprint.
      A line item reads as its posting's own note first, then the account's name metadata
      (accounts set <account> -meta name=HST), then the account's last path segment -- so two legs
      whose accounts end in the same segment, a rent and its tax on one unit, stay told apart.
      -format defaults to text at a terminal and json off one, the same structured fields a
      script or an agent filing the document elsewhere would otherwise have to parse from text.
`},
		{[]string{"report"}, `  report [-basis cash|accrual] [-format text|html|json] [-account <text>] [-from <D>] [-to <D>] [-out <file>]
      Fold the books into the company's full picture: an income statement over the period and a
      balance sheet as of its end, together. -basis reads the one log as cash or accrual, chosen
      at read time and stored nowhere. Within each section, a subsection that gathers more than
      one account -- the level below the section, "Expenses:Real Estate" under Expenses -- is
      subtotalled per commodity, so a reader sees what each grouping came to without adding the
      lines by hand. -format defaults to text at a terminal and json off one.
  report income [-basis cash|accrual] [-format text|html|json] [-account <text>] [-from <D>] [-to <D>] [-out <file>]
      Just the income statement: what was earned and spent over the period, by account, with the
      net per commodity.
  report balance [-basis cash|accrual] [-format text|html|json] [-account <text>] [-from <D>] [-to <D>] [-out <file>]
      Just the balance sheet: assets held and liabilities owed as of -to (or today), and net worth.
  report gains [-basis cash|accrual] [-format text|html|json] [-account <text>] [-from <D>] [-to <D>] [-out <file>]
      Just the capital-gains schedule: a disposal per row -- date, shares, proceeds, cost base, and
      realized gain -- with the total, for a filing (Canada's Schedule 3, the T5008 world).
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

// openReader is open for commands that only fold the log. It takes no lock, so a query still runs
// while an import holds the log open for writing.
func openReader() (*eventlog.Log, func() error, error) {
	s, err := store.OpenReader(".")
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
	// `import -all` imports every registered connector, grouping shared logins; it takes no file, so it
	// is routed before the file argument is read.
	if len(args) > 0 && (args[0] == "-all" || args[0] == "--all") {
		return importAllConnectors(args[1:])
	}
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
	history := fs.Int("history", 0, "days of history to read this run (relative; overrides the connector default)")
	fromFlag := fs.String("from", "", "backfill start date, e.g. 2026-02-01 or \"Feb 1, 2026\" (overrides -history)")
	toFlag := fs.String("to", "", "backfill end date; defaults to today when -from is given")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	from, to, err := backfillRange(*fromFlag, *toFlag)
	if err != nil {
		return err
	}
	dir, err := sessionsDir()
	if err != nil {
		return err
	}
	return importConnector(s.Log, c, fetchOpts{
		sessionDir:  dir,
		snapshotDir: filepath.Join(s.Path, "snapshots"),
		interactive: interactiveTerminal(),
		relogin:     *relogin,
		history:     *history,
		from:        from,
		to:          to,
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
	return filepath.Join(base, "bkpr", "sessions"), nil
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
	reportTwins(log, result)
	uncategorizedHint(log)
	return nil
}

// reportTwins checks the lines an import just landed against everything already in the book and says
// so when any of them collide. Every import path calls it: a duplicate does not care which door it
// came through, and the ledger and CSV readers land lines exactly as a connector does. Never fatal --
// a warning about the books is not a reason to fail an import that already succeeded.
func reportTwins(log *eventlog.Log, result books.ImportResult) {
	reportTwinsForIDs(log, result.IDs)
}

// importConnector fetches a registered connector's lines and lands them through the same import
// every file takes, deduped by fingerprint. A bank whose session has expired with no one present to
// sign in again is reported as an actionable hint rather than a raw error.
func importConnector(log *eventlog.Log, c books.Connector, o fetchOpts) error {
	// A live status while the browser works, so the import does not look like a hang. It stops the
	// moment the fetch returns, before any summary prints, and is silent off a terminal.
	sp := newSpinner(os.Stderr, isTerminal(os.Stderr), "connecting to "+c.Name)
	defer sp.finish()
	o.progress = sp.set

	fetch, err := fetcherFor(c.Kind, o)
	if err != nil {
		return err
	}
	label := "connector:" + c.Name
	var got fetchResult
	err = importFrom(log, label, func() ([]model.Transaction, error) {
		r, ferr := fetch(c)
		sp.finish() // browser work done; stop before importFrom prints its summary
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
		bal := reconcileBalance(c.Account, got.balance)
		if err := books.AssertBalance(log, label, c.Account, time.Now(), bal); err != nil {
			return err
		}
		fmt.Printf("bank balance recorded: %s reconciles %s\n", bal, c.Account)
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
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	txs, entries, err := source.ReadLedger(bytes.NewReader(raw), filepath.Base(path))
	if err != nil {
		return err
	}
	accounts, err := source.ReadLedgerAccounts(bytes.NewReader(raw))
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
	metaSet, err := books.ImportAccountMeta(log, actor, accounts)
	if err != nil {
		return err
	}
	fmt.Printf("%d entries read: %d imported, %d already in the log; %d categorized from the file, %d left to the rules; %d account(s) described\n",
		len(txs), result.Imported, result.Skipped, carried, skipped, metaSet)
	reportTwins(log, result)
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

	txs, err := source.ReadCSV(statement, m, filepath.Base(path))
	if err != nil {
		return err
	}

	result, err := books.Import(log, "statement:"+filepath.Base(path), txs)
	if err != nil {
		return err
	}

	fmt.Printf("%d lines read: %d imported, %d already in the log\n", len(txs), result.Imported, result.Skipped)
	reportTwins(log, result)
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
	creds := credFlag{}
	fs.StringVar(&c.Kind, "kind", "rentapp", "which connector this is")
	fs.StringVar(&c.URL, "url", "", "the connector's base URL")
	fs.StringVar(&c.TokenEnv, "token-env", "", "the environment variable holding its bearer token")
	fs.StringVar(&c.Account, "account", "", "the ledger account its transactions land in")
	fs.StringVar(&c.Currency, "currency", "CAD", "the currency of its transactions")
	fs.Var(&creds, "cred", "a credential reference field=ref (repeatable), e.g. password=op://Private/RBC/password")
	fs.StringVar(&c.SecretCmd, "secret-cmd", "", "command resolving a credential reference; {} is the reference (default: op read {})")
	var path pathFlag
	fs.Var(&path, "account-path", "a link/button to click after sign-in to reach the account (repeatable, in order), e.g. -account-path \"Current Account\"")
	fs.IntVar(&c.HistoryDays, "history", 0, "days of history to read (0 = the site's short default; e.g. 120 for ~4 months)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	c.Name = name
	if len(creds) > 0 {
		c.Credentials = creds
	}
	if len(path) > 0 {
		c.AccountPath = path
	}

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
	log, closeLog, err := openReader()
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

// credFlag collects repeated -cred field=ref pairs into a connector's credential references. The
// value is a secret reference (op://..., a Keychain name), kept verbatim; it is never the secret.
type credFlag map[string]string

func (c credFlag) String() string { return "" }

func (c *credFlag) Set(s string) error {
	i := strings.Index(s, "=")
	if i < 0 {
		return fmt.Errorf("credential %q must be field=reference", s)
	}
	field := strings.TrimSpace(s[:i])
	if field == "" {
		return fmt.Errorf("credential %q has an empty field name", s)
	}
	if *c == nil {
		*c = credFlag{}
	}
	(*c)[field] = strings.TrimSpace(s[i+1:])
	return nil
}

// pathFlag collects repeated -account-path labels, in order, into a connector's account path -- the
// menu clicks that reach an account with no stable URL.
type pathFlag []string

func (p pathFlag) String() string { return "" }

func (p *pathFlag) Set(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("an account-path step cannot be empty")
	}
	*p = append(*p, s)
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
	fs.StringVar(&r.Source, "source", "", "route the matched line's card/liability leg to a sub-account, e.g. \"Liabilities:PC Mastercard:9 Birch Street\"; the parent it rolls up to still reconciles to the one bank balance")
	amount := fs.String("amount", "", "narrow the rule to lines of this magnitude, e.g. 175 (a bare amount is read as CAD; \"175 USD\" names another); its pattern and amount together are the rule's identity")
	fs.StringVar(&r.TaxRate, "tax-rate", "", "sales tax the total already includes, e.g. 15%; splits the tax onto -tax-account")
	fs.StringVar(&r.TaxAccount, "tax-account", "", "account the extracted tax posts to, e.g. \"Assets:HST ITC\"; may reference -tax-category capture groups ($1)")
	fs.StringVar(&r.TaxCategory, "tax-category", "", "scope the tax to lines whose category matches this pattern; a category outside it stays gross")
	taxFrom := fs.String("tax-from", "", "apply the tax only to lines dated on or after this date, e.g. 2026-01-01; earlier lines stay gross")
	fs.Var(&meta, "meta", "key=value carried onto the entry, repeatable, e.g. rentapp.lease=31")
	before := fs.String("before", "", "on a new rule, place it ahead of the one matching this pattern")
	why := fs.String("why", "", "why the rule changed; changing one reclassifies every line it matched")
	actor := fs.String("actor", "human", "who is authoring this rule; the log records who decided")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if r.Amount, err = parseRuleAmount(*amount); err != nil {
		return err
	}
	if *taxFrom != "" {
		if r.TaxFrom, err = taxFromDate(*taxFrom); err != nil {
			return err
		}
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

	warnUnrootedSource(log, r.Source)
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
// parseRuleAmount reads a rule's -amount value into an amount predicate, or nil when none was given.
// A value that names its commodity ("175 USD") is taken as written; a bare number ("175") is read as
// CAD, the book's functional currency, so the common case stays terse.
func parseRuleAmount(s string) (*model.Amount, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var a model.Amount
	var err error
	if len(strings.Fields(s)) >= 2 {
		a, err = model.ParseAmount(s)
	} else {
		a, err = model.NewAmount(s, "CAD")
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// taxFromDate reads a -tax-from value in the written forms every date flag takes and restates it in
// the one form a rule stores, 2006-01-02, so the engine reads one layout however the date was typed.
func taxFromDate(s string) (string, error) {
	t, err := parseAsOf(s)
	if err != nil {
		return "", err
	}
	return t.Format("2006-01-02"), nil
}

func upsertRule(log *eventlog.Log, r rules.Rule, provided map[string]bool, before, why, actor string) error {
	current, err := books.Rules(log)
	if err != nil {
		return err
	}
	for _, existing := range current {
		if !books.SameRule(existing, r) {
			continue
		}
		merged := existing
		if provided["category"] {
			merged.Category = r.Category
		}
		if provided["payee"] {
			merged.Payee = r.Payee
		}
		if provided["source"] {
			merged.Source = r.Source
		}
		if provided["tax-rate"] {
			merged.TaxRate = r.TaxRate
		}
		if provided["tax-account"] {
			merged.TaxAccount = r.TaxAccount
		}
		if provided["tax-category"] {
			merged.TaxCategory = r.TaxCategory
		}
		if provided["tax-from"] {
			merged.TaxFrom = r.TaxFrom
		}
		if provided["meta"] {
			merged.Metadata = mergeMeta(merged.Metadata, r.Metadata)
		}
		if err := validRule(merged); err != nil {
			return err
		}
		return books.SetRule(log, actor, why, merged)
	}
	if err := validRule(r); err != nil {
		return err
	}
	return books.AddRule(log, actor, r, before)
}

// validRule refuses a rule the engine could not run, so an invalid pattern or a half-specified tax
// (a rate with no account, or the reverse) is caught at authoring rather than breaking every later
// read of the books. rules.New is the one authority on what a valid rule is.
func validRule(r rules.Rule) error {
	_, err := rules.New([]rules.Rule{r})
	return err
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
	match, rest, err := firstArg(args, "the rule to remove, by its match pattern")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("rules rm", flag.ExitOnError)
	amount := fs.String("amount", "", "the amount predicate of the variant to remove, when the pattern has several")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	amt, err := parseRuleAmount(*amount)
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
	if err := books.RemoveRule(log, "human", match, amt); err != nil {
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
	amount := fs.String("amount", "", "the amount predicate of the variant to move, when the pattern has several")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	amt, err := parseRuleAmount(*amount)
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
	if err := books.MoveRule(log, "human", match, amt, *before); err != nil {
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
	log, closeLog, err := openReader()
	if err != nil {
		return err
	}
	defer closeLog()

	set, err := books.Rules(log)
	if err != nil {
		return err
	}
	renderRules(os.Stdout, set)
	return nil
}

// renderRules writes the rule set as a table. The amount column carries a rule's predicate when it
// has one, so two rules on one pattern that split by amount read as distinct rows rather than
// identical ones.
func renderRules(out io.Writer, set []rules.Rule) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MATCH\tAMOUNT\tPAYEE\tCATEGORY\tSOURCE")
	for _, r := range set {
		amount := ""
		if r.Amount != nil {
			amount = r.Amount.String()
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Match, amount, r.Payee, r.Category, r.Source)
	}
	w.Flush()
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

// taxedPostings turns the -category and -tax-rate/-tax-account flags into the two postings a taxed
// line asserts: the pre-tax amount on the category and the tax extracted from the same total on the
// tax account, exactly as a taxed rule splits (net = total / (1 + rate), the tax takes the exact
// remainder). One arithmetic serves both forms, so a hand categorization can never drift a cent
// from what the rule form would have booked.
func taxedPostings(category, rate, taxAccount string, line model.Amount) ([]model.Posting, error) {
	numer, denom, err := model.ParsePercent(rate)
	if err != nil {
		return nil, err
	}
	net, tax := line.Negate().SplitInclusive(numer, denom)
	return []model.Posting{{Account: category, Amount: net}, {Account: taxAccount, Amount: tax}}, nil
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
	invoice := fs.String("invoice", "", "the invoice or bill number for this entry, the ledger (code); \"next\" takes the one after the highest the book has issued")
	source := fs.String("source", "", "route this line's card/liability leg to a sub-account, e.g. \"Liabilities:PC Mastercard:9 Birch Street\"; the parent it rolls up to still reconciles to the one bank balance")
	why := fs.String("why", "", "why this line is categorized so; recorded with the assertion")
	gain := fs.String("gain", "", "on a sale, the account its capital gain or loss lands in, e.g. Income:Capital Gains")
	taxRate := fs.String("tax-rate", "", "sales tax the -category total already includes, e.g. 15%; splits the tax onto -tax-account, the same arithmetic a taxed rule uses")
	taxAccount := fs.String("tax-account", "", "account the extracted tax posts to, e.g. \"Assets:HST ITC\"")
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
	case (*taxRate == "") != (*taxAccount == ""):
		return fmt.Errorf("-tax-rate and -tax-account are required together, as on a rule")
	case *taxRate != "" && *category == "":
		return fmt.Errorf("-tax-rate splits a -category total; with -post you spell the legs yourself")
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

	var post []model.Posting
	if *taxRate != "" {
		post, err = taxedPostings(*category, *taxRate, *taxAccount, tx.Amount)
	} else {
		post, err = postingsFor(*category, split, tx.Amount)
	}
	if err != nil {
		return err
	}

	// "next" is resolved here, against the book the assertion is about to land in, so tagging a line
	// with an invoice you are issuing takes the same number the document does.
	number, err := invoiceNumber(s.Log, *invoice)
	if err != nil {
		return err
	}

	warnUnrootedSource(s.Log, *source)
	// -post legs are the caller's own arithmetic, so they are recorded as spelled and no rule's tax
	// overlay may restate them; a -category assertion, whose leg the tool derived, stays open to one.
	assert := books.Categorize
	if len(split) > 0 {
		assert = books.CategorizePosts
	}
	if err := assert(s.Log, *actor, *why, txID, number, *payee, *source, post); err != nil {
		return err
	}
	// tx.ID rather than the argument: a quoted prefix echoes back as the whole fingerprint.
	fmt.Printf("categorized %s\n", tx.ID)
	// A twin created weeks ago by a different door sits quietly in Uncategorized until someone
	// categorizes it -- the same collision FR-17 catches at import time, just found later. Check the
	// line just categorized against the whole book, same as an import checks the lines it just landed.
	reportTwinsForIDs(s.Log, []string{tx.ID})
	return nil
}

// warnUnrootedSource flags a routed -source that has no owned ancestor to roll up into. Routing the
// card leg to a sub-account is what lets a purpose-split card still reconcile to one bank balance,
// but only when the child sits under an account that reconciles -- the registered parent. A source
// hanging off nothing you hold will not tie out, so it is worth a heads-up. It never refuses: a
// deliberate routing elsewhere is the author's call, and the source becomes owned once asserted.
func warnUnrootedSource(log *eventlog.Log, source string) {
	if source == "" {
		return
	}
	owned, err := books.OwnedAccounts(log)
	if err != nil {
		return
	}
	for a := range owned {
		if a != source && strings.HasPrefix(source, a+":") {
			return // a reconciling ancestor already holds it
		}
	}
	fmt.Fprintf(os.Stderr, "warning: -source %q sits under no account you hold, so it will not roll up to a bank balance in reconcile\n", source)
}

// commentCmd leaves a free-text note on one posting of a line, or clears it with -remove. The note
// is commentary the books carry alongside the account and amount; it renders inline in the ledger
// artifact and reads back, so a reason left on a split is durable. -account names which leg the note
// belongs to and may be omitted only when the line has a single posting.
func commentCmd(args []string) error {
	txID, rest, err := firstArg(args, "the transaction fingerprint to comment on")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("comment", flag.ExitOnError)
	account := fs.String("account", "", "which posting the note belongs to; needed only when the line splits across several")
	text := fs.String("text", "", "the note to leave on the posting")
	remove := fs.Bool("remove", false, "clear the note from the posting instead of setting one")
	why := fs.String("why", "", "why the note is left; recorded with the assertion")
	actor := fs.String("actor", "human", "who is commenting; the log records who decided")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if *remove == (*text != "") {
		return fmt.Errorf("give -text to leave a note, or -remove to clear one, not both or neither")
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	if err := books.Comment(s.Log, *actor, *why, txID, *account, *text, *remove); err != nil {
		return err
	}
	fmt.Printf("commented %s\n", txID)
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

// unvoidCmd reverses a mistaken void, the way to undo a bad void.
func unvoidCmd(args []string) error {
	txID, rest, err := firstArg(args, "the transaction fingerprint to unvoid")
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("unvoid", flag.ExitOnError)
	why := fs.String("why", "", "why the void is being reversed; recorded with the unvoid")
	actor := fs.String("actor", "human", "who is unvoiding; the log records who decided")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	if err := books.UnvoidTransaction(s.Log, *actor, *why, txID); err != nil {
		return err
	}
	fmt.Printf("unvoided %s\n", txID)
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

// invoiceCmd dispatches `invoice raise|settle|void|list|aging|next`. An invoice is a first-class thing you do,
// so it is its own namespace, the way rules and connectors are, rather than a subtype of a more
// abstract verb.
func invoiceCmd(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("invoice needs raise, settle, void, list, aging, or next")
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
	case "next":
		return invoiceNext(args[1:])
	default:
		usage()
		return fmt.Errorf("unknown invoice subcommand %q", args[0])
	}
}

// invoiceNumber resolves what was given for -invoice. The literal "next" is read out of the book at
// the moment the command runs, so nobody has to hold the sequence in their head; anything else is
// the caller's own number, kept verbatim. It is deliberately not offered on bill receive: that
// number is the vendor's, and issuing one of ours there would both mislabel the bill and spend a
// number of ours on it.
func invoiceNumber(log *eventlog.Log, number string) (string, error) {
	if number != "next" {
		return number, nil
	}
	return books.NextInvoiceNumber(log)
}

// invoiceNext prints the number the next invoice would take, and nothing else, so the same command
// answers a person and a script filling in a document by hand.
func invoiceNext(args []string) error {
	fs := flag.NewFlagSet("invoice next", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	log, closeLog, err := openReader()
	if err != nil {
		return err
	}
	defer closeLog()

	number, err := books.NextInvoiceNumber(log)
	if err != nil {
		return err
	}
	fmt.Println(number)
	return nil
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
	number := fs.String("invoice", "", "the invoice number, rendered as the ledger (code); \"next\" takes the one after the highest the book has issued")
	taxRate := fs.String("tax-rate", "", "the sales tax the amount already includes, e.g. 15%; needs -tax-account")
	taxAccount := fs.String("tax-account", "", "where the collected tax is owed from until it is remitted")
	description := fs.String("description", "", "what is being billed, e.g. \"Rent for Aug 1\"; labels the line on the document")
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

	// The number is resolved against the book the invoice is about to be recorded in, so "next" reads
	// the sequence as it stands now. It is not part of the fingerprint, so re-running the same raise
	// still collapses to the one invoice and leaves the number it was first given alone.
	num, err := invoiceNumber(log, *number)
	if err != nil {
		return err
	}

	inv, added, err := books.Raise(log, *actor, *why, books.Invoice{
		Date: when, Party: *party, Amount: amt, Category: *category, Account: *account, Number: num,
		TaxRate: *taxRate, TaxAccount: *taxAccount, Description: *description,
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
	log, closeLog, err := openReader()
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
	number := fs.String("invoice", "", "the bill number (the vendor's invoice number), rendered as the ledger (code)")
	taxRate := fs.String("tax-rate", "", "the sales tax the amount already includes, e.g. 15%; needs -tax-account")
	taxAccount := fs.String("tax-account", "", "where the tax is claimed back through")
	description := fs.String("description", "", "what is being billed, e.g. \"Dumpster rental\"; labels the line on the document")
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
		Date: when, Party: *party, Amount: amt, Category: *category, Account: *account, Number: *number,
		TaxRate: *taxRate, TaxAccount: *taxAccount, Description: *description,
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
	log, closeLog, err := openReader()
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
	log, closeLog, err := openReader()
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
	log, closeLog, err := openReader()
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
	log, closeLog, err := openReader()
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

// accountCmd dispatches `accounts set|list|due`: the metadata an account carries (a letterhead
// address, a customer's mailing address, a display name, a due date) that a document like an
// invoice reads, and the due report that reads it back for liability accounts.
func accountCmd(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("accounts needs set, list, or due")
	}
	switch args[0] {
	case "set":
		return accountSetMeta(args[1:])
	case "list":
		return accountList(args[1:])
	case "due":
		return accountDue(args[1:])
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
	fs.Var(&meta, "meta", "key=value, repeatable; e.g. -meta name=\"Northwind Studio\" -meta address=\"123 Main St\"")
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
	fs := flag.NewFlagSet("accounts list", flag.ExitOnError)
	sortBy := fs.String("sort", "", "sort by: amount (the default is by account name)")
	desc := fs.Bool("desc", false, "sort descending (with -sort amount)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *sortBy != "" && *sortBy != "amount" {
		return fmt.Errorf("accounts list -sort takes: amount (the default is by name)")
	}

	log, closeLog, err := openReader()
	if err != nil {
		return err
	}
	defer closeLog()

	owned, err := books.OwnedAccounts(log)
	if err != nil {
		return err
	}
	balances, err := books.ReconciledBalances(log)
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
	sort.Strings(accounts) // by name: the default, and the tie-break when sorting by amount
	if *sortBy == "amount" {
		accounts = orderAccountsByAmount(accounts, balances, *desc)
	}

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

// accountDue reports which liability accounts (credit cards, LOCs) owe money right now, alongside
// the due date and minimum payment recorded on each via `accounts set -meta due=... minimum=...`.
// It is the accounts-list-adjacent view invoice aging and bill aging have for receivables and
// payables, but for the liability accounts themselves: a routine bookwork pass can ask "which
// accounts need a payment soon and how much" without eyeballing balances and the bank site by hand.
func accountDue(args []string) error {
	fs := flag.NewFlagSet("accounts due", flag.ExitOnError)
	format := fs.String("format", defaultFormat(os.Stdout, "table", "json"), "table or json (default: table at a terminal, json off one)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *format != "table" && *format != "json" {
		return fmt.Errorf("accounts due -format takes table or json")
	}

	log, closeLog, err := openReader()
	if err != nil {
		return err
	}
	defer closeLog()

	rows, err := books.DueAccounts(log)
	if err != nil {
		return err
	}

	if *format == "json" {
		return renderDueJSON(rows)
	}
	return printDue(rows)
}

// printDue is accountDue's human form: one row per owing liability account, a dash standing in for
// a due date or minimum nobody has set yet.
func printDue(rows []books.DueAccount) error {
	if len(rows) == 0 {
		fmt.Println("no liability accounts owing")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ACCOUNT\tBALANCE\tDUE\tMINIMUM")
	for _, r := range rows {
		due, min := r.Due, r.Minimum
		if due == "" {
			due = "-"
		}
		if min == "" {
			min = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Account, balanceCell(r.Balance), due, min)
	}
	return w.Flush()
}

// dueAccountJSON is accountDue's machine-readable form: a balance rendered the same way jsonAmounts
// renders one everywhere else in this tool (a list of "amount commodity" strings), so a mixed-
// commodity account never needs the reader to reconstruct an amount from parts.
type dueAccountJSON struct {
	Account string   `json:"account"`
	Balance []string `json:"balance"`
	Due     string   `json:"due,omitempty"`
	Minimum string   `json:"minimum,omitempty"`
}

func renderDueJSON(rows []books.DueAccount) error {
	out := make([]dueAccountJSON, 0, len(rows))
	for _, r := range rows {
		commodities := make([]string, 0, len(r.Balance))
		for c := range r.Balance {
			commodities = append(commodities, c)
		}
		sort.Strings(commodities)
		balance := make([]string, 0, len(commodities))
		for _, c := range commodities {
			balance = append(balance, r.Balance[c].String())
		}
		out = append(out, dueAccountJSON{Account: r.Account, Balance: balance, Due: r.Due, Minimum: r.Minimum})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
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
		if r.Stale {
			return "as of " + r.AsOf.Format("2006-01-02") + ", not checked since"
		}
		return "yes, as of " + r.AsOf.Format("2006-01-02")
	}
	return fmt.Sprintf("off by %s (as of %s)", r.Delta, r.AsOf.Format("2006-01-02"))
}

// reconcileCmd shows every account with a scraped balance against the books: what the bank last said
// it held, what the books fold to on that date, and the difference. It writes nothing; it is a fold.
func reconcileCmd(args []string) error {
	fs := flag.NewFlagSet("reconcile", flag.ExitOnError)
	history := fs.String("history", "", "show every bank-asserted balance on record for one account, in date order, instead of just the latest -- so a delta can be bisected to the assertion that first introduced it")
	if err := fs.Parse(args); err != nil {
		return err
	}

	log, closeLog, err := openReader()
	if err != nil {
		return err
	}
	defer closeLog()

	if *history != "" {
		return reconcileHistoryCmd(log, *history)
	}

	recs, err := books.Reconcile(log)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		fmt.Println("no bank balances recorded yet; import a bank connector to record one")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ACCOUNT\tCHECKED\tBANK\tBOOKS\tDELTA")
	allReconciled := true
	var stale, anchored []string
	for _, r := range recs {
		status := r.Delta.String()
		if r.Reconciled {
			status = "0 (reconciled)"
		} else {
			allReconciled = false
		}
		if r.Stale {
			status += ", not checked since"
			stale = append(stale, r.Account)
		}
		// The window the verdict covers, not just the date it ends. Everything before the first
		// assertion is inside the derived opening balance, so it is anchored rather than checked, and an
		// account measured only once has not been checked at all.
		window := r.Since.Format("2006-01-02") + " → " + r.AsOf.Format("2006-01-02")
		if r.Anchored {
			window = "anchored " + r.AsOf.Format("2006-01-02")
			status = "0 (by construction)"
			anchored = append(anchored, r.Account)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Account, window, r.Bank, r.Books, status)
	}
	w.Flush()
	if len(anchored) > 0 {
		fmt.Printf("\n%d account(s) carry a single bank figure, which derived their opening balance: they agree by\nconstruction and nothing has tested them yet.\n", len(anchored))
		for _, a := range anchored {
			fmt.Println("  " + a)
		}
	}
	// A stale anchor is not a mismatch, so it must not read as one -- but it must not be swallowed by an
	// unqualified all-clear either: the account agreed with the bank on its AS OF date and has not been
	// measured since, which is a different claim from the one the other rows are making.
	if len(stale) > 0 {
		fmt.Printf("\n%d account(s) last measured against the bank before the rest of the books; nothing has checked them since:\n", len(stale))
		for _, a := range stale {
			fmt.Println("  " + a)
		}
	}
	if allReconciled && len(stale) == 0 {
		fmt.Println("\nall accounts reconcile to the penny")
	} else if allReconciled {
		fmt.Println("\nevery account reconciles to the penny as of the date shown")
	}
	return nil
}

// reconcileHistoryCmd shows every bank-asserted balance on record for one account, in date order, each
// checked against the books the same way reconcile's single current-moment row is: what the bank said,
// what the books fold to as of that same date, and the difference. It lets a delta that only shows up
// in the latest check be bisected to the specific assertion -- the specific import -- that first
// introduced it, without a fresh manual statement or reading the log directly.
func reconcileHistoryCmd(log *eventlog.Log, account string) error {
	hist, err := books.ReconcileHistory(log, account)
	if err != nil {
		return err
	}
	if len(hist) == 0 {
		fmt.Printf("no bank balances recorded for %s yet; import a bank connector to record one\n", account)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "AS OF\tBANK\tBOOKS\tDELTA")
	firstBad := ""
	for _, r := range hist {
		status := r.Delta.String()
		if r.Reconciled {
			status = "0 (reconciled)"
		} else if firstBad == "" {
			firstBad = r.AsOf.Format("2006-01-02")
		}
		if r.Anchored {
			status = "0 (by construction)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.AsOf.Format("2006-01-02"), r.Bank, r.Books, status)
	}
	w.Flush()
	if firstBad == "" {
		fmt.Printf("\n%s reconciles to the penny at every bank-asserted balance on record\n", account)
	} else {
		fmt.Printf("\n%s first shows a delta as of %s; that assertion's import is where to start looking\n", account, firstBad)
	}
	return nil
}
