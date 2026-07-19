package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dallasread/bkpr/lib/adapters/ledger"
	"github.com/dallasread/bkpr/lib/books"
	"github.com/dallasread/bkpr/lib/model"
	"github.com/dallasread/bkpr/lib/store"
)

// accountsFlag collects repeated -account patterns, so one reading can name several accounts and
// keep every line posting to any of them.
type accountsFlag []string

func (a *accountsFlag) String() string { return "" }

func (a *accountsFlag) Set(s string) error {
	*a = append(*a, s)
	return nil
}

// renderBooks folds the log and renders it: a table or JSON to read, or the ledger artifact.
// -account narrows the reading to the lines posting to a matching account, so there is no separate
// review command: the decision queue is `books -account Uncategorized`, and any other account
// question is the same machinery with a different pattern.
func renderBooks(args []string) error {
	fs := flag.NewFlagSet("books", flag.ExitOnError)
	var accounts accountsFlag
	format := fs.String("format", defaultFormat(os.Stdout, "table", "json"), "output format: table, json, or ledger (default: table at a terminal, json off one)")
	basis := fs.String("basis", "cash", "accounting basis: cash or accrual")
	since := fs.String("since", "", "on -basis accrual, book only invoices/bills dated on or after this (YYYY-MM-DD)")
	fs.Var(&accounts, "account", "show only lines posting to an account matching this pattern; repeatable, any match keeps the line")
	from := fs.String("from", "", "show only lines dated on or after this (YYYY-MM-DD)")
	to := fs.String("to", "", "show only lines dated on or before this (YYYY-MM-DD)")
	value := fs.String("value", "", "value every amount in this commodity, e.g. CAD, using recorded @@ prices; pass -rate for lines without one")
	rate := fs.String("rate", "", "per-unit rates for amounts with no recorded price under -value, e.g. USD=1.35 (comma-separated)")
	stdout := fs.Bool("stdout", false, "write the ledger to stdout instead of the store")
	sortBy := fs.String("sort", "", "sort lines by: amount (the default is by date)")
	desc := fs.Bool("desc", false, "sort descending (with -sort amount)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *basis != string(books.CashBasis) && *basis != string(books.AccrualBasis) {
		return fmt.Errorf("unknown basis %q: want cash or accrual", *basis)
	}
	if *sortBy != "" && *sortBy != "amount" {
		return fmt.Errorf("books -sort takes: amount (the default is by date)")
	}
	if *sortBy != "" && *format == "ledger" {
		return fmt.Errorf("-sort cannot reorder -format ledger: the ledger stays in date order, its canonical form")
	}
	rates, err := parseRates(*rate, *value)
	if err != nil {
		return err
	}
	if *value != "" && *format == "ledger" {
		return fmt.Errorf("-value cannot render -format ledger: the ledger is the stored artifact and must stay in each line's own commodity")
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
	fromDay, toDay, err := periodBounds(*from, *to)
	if err != nil {
		return err
	}

	s, err := store.OpenReader(".")
	if err != nil {
		return err
	}
	defer s.Close()

	txs, entries, err := books.LedgerBasisSince(s.Log, books.Basis(*basis), effective)
	if err != nil {
		return err
	}
	if len(accounts) > 0 {
		if txs, entries, err = filterByAccount(accounts, txs, entries); err != nil {
			return err
		}
	}
	if !fromDay.IsZero() || !toDay.IsZero() {
		txs, entries = filterByDate(fromDay, toDay, txs, entries)
	}

	// Under -value the reading is restated into one currency before anything reads it, so every
	// format — the health line and each line's amount alike — speaks that currency from the one fold.
	val := newValuer(*value, rates)
	if *value != "" {
		txs, entries = valued(val, txs, entries)
	}

	// The summary is computed once, here, and handed to whichever renderer runs. That is the
	// mechanism that keeps the formats from drifting: no format computes its own numbers, so the
	// table, the JSON, and the ledger cannot disagree about the same reading.
	sum, err := summarize(txs, entries)
	if err != nil {
		return err
	}

	// The reading's order is a display choice made after the summary is folded, so sorting the lines
	// by amount never changes the totals -- only which line prints first.
	if *sortBy == "amount" {
		txs, entries = orderLinesByAmount(txs, entries, *desc)
	}

	switch *format {
	case "table":
		if err := report(os.Stdout, txs, entries, sum); err != nil {
			return err
		}
		if *value != "" {
			fmt.Fprintf(os.Stdout, "\n%s\n", valuationNote(*value))
		}
		warnUnvalued(val)
		return nil
	case "json":
		if err := writeJSON(os.Stdout, txs, entries, sum); err != nil {
			return err
		}
		warnUnvalued(val)
		return nil
	case "ledger":
		// A filtered ledger is a reading and goes to stdout; the artifact in the store is only
		// ever the whole books, so a partial one can never overwrite it.
		if *stdout || len(accounts) > 0 || !fromDay.IsZero() || !toDay.IsZero() {
			if err := ledger.WriteAll(os.Stdout, txs, entries); err != nil {
				return err
			}
			return writeSummaryComments(os.Stdout, sum)
		}
		return writeLedger(s, txs, entries, sum)
	default:
		return fmt.Errorf("unknown format %q: want table, json, or ledger", *format)
	}
}

// bookSummary is the one health reading of the books: what came in, what went out, what is left,
// and how much money the rules could not even place a kind on. Every format renders this same
// struct, so the reading cannot differ by format.
type bookSummary struct {
	Lines              int
	UncategorizedLines int
	Totals             []commodityTotals
}

// commodityTotals is the health line for one commodity. Amounts of different commodities cannot
// be summed, so a mixed book carries one line per commodity rather than a total that lies.
type commodityTotals struct {
	Commodity     string
	Income        model.Amount // money in, as a positive magnitude
	Expenses      model.Amount // money out, as a positive magnitude
	Net           model.Amount // income less expenses
	Uncategorized model.Amount // money whose kind is unknown, in statement sign: money out reads negative
}

// summarize folds the (possibly filtered) reading into its health line, counting each line's own
// account alongside its postings: a hand-kept line can carry its expense or income there. Income
// carries the negation of the deposit, so it is negated back to a magnitude; a bare Uncategorized
// posting is money whose kind is unknown and is reported in statement sign, because calling it
// income or expense is exactly the guess the books refuse to make.
func summarize(txs []model.Transaction, entries []model.Entry) (bookSummary, error) {
	sum := bookSummary{Lines: len(txs)}
	totals := map[string]*commodityTotals{}
	forCommodity := func(commodity string) *commodityTotals {
		t, ok := totals[commodity]
		if !ok {
			zero := model.Amount{Commodity: commodity}
			t = &commodityTotals{Commodity: commodity, Income: zero, Expenses: zero, Net: zero, Uncategorized: zero}
			totals[commodity] = t
		}
		return t
	}
	fold := func(account string, amount model.Amount) error {
		t := forCommodity(amount.Commodity)
		var err error
		switch {
		case account == model.Uncategorized:
			t.Uncategorized, err = t.Uncategorized.Add(amount.Negate())
		case topLevel(account) == "Income":
			t.Income, err = t.Income.Add(amount.Negate())
		case topLevel(account) == "Expenses":
			t.Expenses, err = t.Expenses.Add(amount)
		default:
			// assets, liabilities, and priced share postings are not the income statement
		}
		return err
	}

	for i := range txs {
		e := entries[i]
		if e.Uncategorized() {
			sum.UncategorizedLines++
		}
		if err := fold(txs[i].Account, txs[i].Amount); err != nil {
			return bookSummary{}, err
		}
		for _, p := range e.Postings {
			if err := fold(p.Account, p.Amount); err != nil {
				return bookSummary{}, err
			}
		}
	}

	for _, t := range totals {
		net, err := t.Income.Add(t.Expenses.Negate())
		if err != nil {
			return bookSummary{}, err
		}
		t.Net = net
		if !t.Income.IsZero() || !t.Expenses.IsZero() || !t.Uncategorized.IsZero() {
			sum.Totals = append(sum.Totals, *t)
		}
	}
	sort.Slice(sum.Totals, func(i, j int) bool { return sum.Totals[i].Commodity < sum.Totals[j].Commodity })
	return sum, nil
}

func topLevel(account string) string {
	if i := strings.Index(account, ":"); i >= 0 {
		return account[:i]
	}
	return account
}

// filterByAccount keeps the lines with a posting whose account matches any of the patterns,
// case-insensitively, anywhere in the path. Ledger matches on the whole account name the same way,
// which is what lets one pattern find Uncategorized at every depth: bare, or as a truncated leaf.
func filterByAccount(patterns []string, txs []model.Transaction, entries []model.Entry) ([]model.Transaction, []model.Entry, error) {
	res := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			return nil, nil, fmt.Errorf("-account %q is not a valid pattern: %v", p, err)
		}
		res[i] = re
	}

	var keptTxs []model.Transaction
	var keptEntries []model.Entry
	for i, tx := range txs {
		if postsToAny(entries[i], res) {
			keptTxs = append(keptTxs, tx)
			keptEntries = append(keptEntries, entries[i])
		}
	}
	return keptTxs, keptEntries, nil
}

// periodBounds parses -from and -to. Both ends are inclusive: -from 2026-03-01 -to 2026-03-31 is
// exactly March, which is how a person names a month. This narrows the reading by date; it is not
// -since, which chooses how far back the accrual basis books invoices and bills at all.
func periodBounds(from, to string) (time.Time, time.Time, error) {
	var fromDay, toDay time.Time
	var err error
	if from != "" {
		if fromDay, err = time.Parse("2006-01-02", from); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("-from %q is not YYYY-MM-DD", from)
		}
	}
	if to != "" {
		if toDay, err = time.Parse("2006-01-02", to); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("-to %q is not YYYY-MM-DD", to)
		}
	}
	if !fromDay.IsZero() && !toDay.IsZero() && toDay.Before(fromDay) {
		return time.Time{}, time.Time{}, fmt.Errorf("-to %s is before -from %s", to, from)
	}
	return fromDay, toDay, nil
}

// filterByDate keeps the lines dated inside the period, both ends inclusive; a zero end is open.
func filterByDate(from, to time.Time, txs []model.Transaction, entries []model.Entry) ([]model.Transaction, []model.Entry) {
	var keptTxs []model.Transaction
	var keptEntries []model.Entry
	for i, tx := range txs {
		if !from.IsZero() && tx.Date.Before(from) {
			continue
		}
		if !to.IsZero() && tx.Date.After(to) {
			continue
		}
		keptTxs = append(keptTxs, tx)
		keptEntries = append(keptEntries, entries[i])
	}
	return keptTxs, keptEntries
}

// valued restates every amount in the reading into val's target so a mixed-commodity book reads in
// one currency. A posting is valued at its own recorded @@ price (or a supplied -rate); the source
// line, which records no price, is valued as the negation of its postings when they all reached the
// target — a line and its categorized side sum to zero — and otherwise at a -rate. It returns copies,
// leaving the stored reading untouched, which is why -value never restates the ledger artifact.
func valued(val *valuer, txs []model.Transaction, entries []model.Entry) ([]model.Transaction, []model.Entry) {
	if val == nil || val.target == "" {
		return txs, entries
	}
	vt := make([]model.Transaction, len(txs))
	ve := make([]model.Entry, len(entries))
	for i, tx := range txs {
		e := entries[i]
		ps := make([]model.Posting, len(e.Postings))
		side := model.Amount{Commodity: val.target}
		allTarget := len(e.Postings) > 0
		for j, p := range e.Postings {
			amt := val.restate(p.Amount, p.Cost)
			ps[j] = model.Posting{Account: p.Account, Amount: amt}
			switch {
			case amt.Commodity != val.target:
				allTarget = false
				if p.Cost != nil {
					ps[j].Cost = p.Cost // left in its own commodity: keep the price it still carries
				}
			default:
				if sum, err := side.Add(amt); err == nil {
					side = sum
				}
			}
		}
		switch {
		case tx.Amount.Commodity == val.target:
			// already in the target; the source line needs no valuing
		case allTarget:
			tx.Amount = side.Negate()
		default:
			tx.Amount = val.restate(tx.Amount, nil)
		}
		e.Postings = ps
		vt[i] = tx
		ve[i] = e
	}
	return vt, ve
}

// valuationNote explains, under -value, that the amounts shown were restated into the target — a line
// at its recorded @@ price or a supplied -rate — so a reader does not mistake a valued figure for the
// amount originally recorded. A line that could be valued neither way stays in its own currency, which
// warnUnvalued then names.
func valuationNote(target string) string {
	return fmt.Sprintf("amounts valued in %s from each line's recorded @@ price or a -rate; a line with neither is left in its own currency", target)
}

func postsToAny(e model.Entry, res []*regexp.Regexp) bool {
	for _, p := range e.Postings {
		for _, re := range res {
			if re.MatchString(p.Account) {
				return true
			}
		}
	}
	return false
}

// report renders the books for a person: the fingerprint first, because it is the handle every
// correction takes, then the line and where it posted, then the health line every format shares.
func report(out io.Writer, txs []model.Transaction, entries []model.Entry, sum bookSummary) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FINGERPRINT\tDATE\tPAYEE\tAMOUNT\tPOSTS TO")
	for i, tx := range txs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			tx.ID, tx.Date.Format("2006-01-02"), entries[i].Payee, tx.Amount, accounts(entries[i]))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	// Every line posts, so the only thing left to say is where the rules ran out, and how to see
	// only those lines.
	if sum.UncategorizedLines > 0 {
		fmt.Fprintf(out, "\n%d lines posted, %d of them uncategorized (bkpr books -account Uncategorized shows only them)\n", sum.Lines, sum.UncategorizedLines)
	} else {
		fmt.Fprintf(out, "\n%d lines posted, %d of them uncategorized\n", sum.Lines, sum.UncategorizedLines)
	}

	if len(sum.Totals) == 0 {
		return nil
	}
	fmt.Fprintln(out)
	t := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(t, "INCOME\tEXPENSES\tNET\tUNCATEGORIZED")
	for _, row := range sum.Totals {
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\n", row.Income, row.Expenses, row.Net, row.Uncategorized)
	}
	return t.Flush()
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

// jsonTotals is one commodity's health line, amounts as the same strings every format prints.
type jsonTotals struct {
	Commodity     string `json:"commodity"`
	Income        string `json:"income"`
	Expenses      string `json:"expenses"`
	Net           string `json:"net"`
	Uncategorized string `json:"uncategorized"`
}

type jsonSummary struct {
	Lines              int          `json:"lines"`
	UncategorizedLines int          `json:"uncategorized_lines"`
	Totals             []jsonTotals `json:"totals"`
}

func writeJSON(w io.Writer, txs []model.Transaction, entries []model.Entry, sum bookSummary) error {
	js := jsonSummary{Lines: sum.Lines, UncategorizedLines: sum.UncategorizedLines}
	for _, t := range sum.Totals {
		js.Totals = append(js.Totals, jsonTotals{
			Commodity: t.Commodity, Income: t.Income.String(), Expenses: t.Expenses.String(),
			Net: t.Net.String(), Uncategorized: t.Uncategorized.String(),
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Lines   []bookLine  `json:"lines"`
		Summary jsonSummary `json:"summary"`
	}{bookLines(txs, entries), js})
}

// writeSummaryComments appends the health line to a ledger rendering as `;` comments, which
// ledger tools and the import reader both ignore, so the artifact stays round-trippable while
// carrying the same reading as every other format.
func writeSummaryComments(w io.Writer, sum bookSummary) error {
	if sum.Lines == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(w, "\n; %d lines posted, %d of them uncategorized\n", sum.Lines, sum.UncategorizedLines); err != nil {
		return err
	}
	for _, t := range sum.Totals {
		if _, err := fmt.Fprintf(w, "; income %s | expenses %s | net %s | uncategorized %s\n",
			t.Income, t.Expenses, t.Net, t.Uncategorized); err != nil {
			return err
		}
	}
	return nil
}

// writeLedger regenerates the artifact in place. The books are a fold, so the ledger is derived
// output: bkpr owns the file and rewrites it whole, and its git diff is the readable account
// of what changed.
func writeLedger(s *store.Store, txs []model.Transaction, entries []model.Entry, sum bookSummary) error {
	meta, err := books.AccountMeta(s.Log)
	if err != nil {
		return err
	}
	f, err := os.Create(s.Ledger())
	if err != nil {
		return err
	}
	// The account directives lead the file, so a hand-kept letterhead round-trips through a rewrite;
	// a blank line sets them off from the entries when any were written.
	var accounts bytes.Buffer
	if err := ledger.WriteAccounts(&accounts, meta); err != nil {
		f.Close()
		return err
	}
	if accounts.Len() > 0 {
		if _, err := fmt.Fprintf(f, "%s\n", accounts.Bytes()); err != nil {
			f.Close()
			return err
		}
	}
	if err := ledger.WriteAll(f, txs, entries); err != nil {
		f.Close()
		return err
	}
	if err := writeSummaryComments(f, sum); err != nil {
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
