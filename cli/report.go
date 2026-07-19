package main

import (
	"flag"
	"fmt"
	"html/template"
	"io"
	"math/big"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dallasread/bkpr/lib/books"
	"github.com/dallasread/bkpr/lib/eventlog"
	"github.com/dallasread/bkpr/lib/model"
	"github.com/dallasread/bkpr/lib/store"
)

// valuer restates amounts into a target commodity for a "report in <target>" lens (the -value flag),
// so foreign income and holdings can be read in the book's own currency. With an empty target it is
// the identity, which is the ordinary per-commodity report.
//
// It never invents a price. It prefers the price a posting already carries — a @@ cost recorded in
// the target, which is the exact figure the books kept for that line — then a per-unit rate the
// caller supplied for the residual. An amount it can value neither way is left in its own commodity,
// and that commodity is remembered so the command can warn which totals are still foreign.
type valuer struct {
	target   string
	rates    map[string]model.Amount // per-unit target price of a foreign commodity
	unpriced map[string]bool         // commodities met that could not be valued
}

// newValuer builds a lens into target, using the supplied per-unit rates for anything without its
// own recorded price. A nil valuer, or one with an empty target, is the identity.
func newValuer(target string, rates map[string]model.Amount) *valuer {
	return &valuer{target: target, rates: rates, unpriced: map[string]bool{}}
}

// restate returns amount expressed in the target commodity, keeping the amount's sign, or returns it
// unchanged when there is no lens or no price to value it with. A @@ cost recorded in the target is a
// total for the whole posting, so it is used as the value directly; a per-unit rate is multiplied
// through. An amount left in its own commodity is remembered as unpriced.
func (v *valuer) restate(amount model.Amount, cost *model.Amount) model.Amount {
	if v == nil || v.target == "" || amount.Commodity == v.target {
		return amount
	}
	if cost != nil && cost.Commodity == v.target {
		priced := *cost
		if priced.Units < 0 {
			priced = priced.Negate()
		}
		if amount.Units < 0 {
			priced = priced.Negate()
		}
		return priced
	}
	if rate, ok := v.rates[amount.Commodity]; ok {
		return convertAt(amount, rate)
	}
	v.unpriced[amount.Commodity] = true
	return amount
}

// convertAt values amount at a per-unit rate — the target-commodity price of one unit of amount's
// commodity — rounded half up to the target's minor unit (two places, cents). It is reached only for
// a residual that carried no recorded price, where a rounded per-unit rate is an accepted estimate;
// a posting that recorded its own @@ total is never routed through here, so no exact basis is rounded.
func convertAt(amount, rate model.Amount) model.Amount {
	const targetScale = 2
	num := new(big.Int).Mul(big.NewInt(amount.Units), big.NewInt(rate.Units))
	num.Mul(num, tenPow(targetScale))
	den := tenPow(int(amount.Scale) + int(rate.Scale))

	neg := num.Sign() < 0
	if neg {
		num.Neg(num)
	}
	num.Add(num, new(big.Int).Div(den, big.NewInt(2))) // round half up on the magnitude
	units := new(big.Int).Quo(num, den).Int64()
	if neg {
		units = -units
	}
	return model.Amount{Units: units, Scale: targetScale, Commodity: rate.Commodity}
}

func tenPow(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

// parseRates reads -rate values into a per-commodity price in the target: "USD=1.35" says one USD is
// worth 1.35 of the target, and several may be given comma-separated. A rate needs a target to value
// into, and cannot price the target against itself.
func parseRates(spec, target string) (map[string]model.Amount, error) {
	rates := map[string]model.Amount{}
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return rates, nil
	}
	if target == "" {
		return nil, fmt.Errorf("-rate needs -value to say which currency to value into")
	}
	for _, pair := range strings.Split(spec, ",") {
		commodity, value, ok := strings.Cut(pair, "=")
		commodity = strings.TrimSpace(commodity)
		if !ok || commodity == "" {
			return nil, fmt.Errorf("-rate %q must be COMMODITY=RATE, e.g. USD=1.35", pair)
		}
		if commodity == target {
			return nil, fmt.Errorf("-rate %q values %s in itself", pair, target)
		}
		amt, err := model.NewAmount(strings.TrimSpace(value), target)
		if err != nil {
			return nil, fmt.Errorf("-rate %q: %w", pair, err)
		}
		rates[commodity] = amt
	}
	return rates, nil
}

// reportRow is one account's total in one commodity, shown the way a statement reads: income earned
// and expenses spent both positive.
type reportRow struct {
	Account string
	Amount  model.Amount
}

// incomeStatement is what the books earned and spent over a period. Totals are per commodity: a USD
// consulting fee and CAD rent do not sum without a price, so the net is reported in each commodity
// on its own rather than forced into one number.
type incomeStatement struct {
	From, To string
	Income   []reportRow
	Expenses []reportRow
	Net      []model.Amount
}

// buildReport folds the entries into an income statement over [from, to] (a zero time is an open
// bound), optionally narrowed to accounts whose path contains filter, so "123 Main" reaches a
// property's income and expenses at once. It sums the Income and Expenses sides wherever they sit:
// usually a posting, but a hand-kept line can carry one as its own account (the importer reads the
// last posting as the source), and both sides are the same money. The accounts a P&L does not show
// (assets, liabilities, equity) are left out.
func buildReport(txs []model.Transaction, entries []model.Entry, from, to time.Time, filter string, val *valuer) incomeStatement {
	incomeRows := map[string]model.Amount{}
	expenseRows := map[string]model.Amount{}
	incomeTotal := map[string]model.Amount{}
	expenseTotal := map[string]model.Amount{}

	fold := func(account string, amount model.Amount, cost *model.Amount) {
		if !matches(account, filter) {
			return
		}
		switch {
		case isUnder(account, "Income"):
			earned := val.restate(amount, cost).Negate() // income is negative-normal; a statement shows it positive
			addAmount(incomeRows, account+"|"+earned.Commodity, earned)
			addAmount(incomeTotal, earned.Commodity, earned)
		case isUnder(account, "Expenses"):
			spent := val.restate(amount, cost)
			addAmount(expenseRows, account+"|"+spent.Commodity, spent)
			addAmount(expenseTotal, spent.Commodity, spent)
		}
	}

	for i, tx := range txs {
		if !inRange(tx.Date, from, to) {
			continue
		}
		fold(entries[i].SourceAccount(tx), tx.Amount, nil)
		for _, p := range entries[i].Postings {
			fold(p.Account, p.Amount, p.Cost)
		}
	}

	stmt := incomeStatement{Income: rowsOf(incomeRows), Expenses: rowsOf(expenseRows)}
	if !from.IsZero() {
		stmt.From = from.Format("2006-01-02")
	}
	if !to.IsZero() {
		stmt.To = to.Format("2006-01-02")
	}

	commodities := map[string]bool{}
	for c := range incomeTotal {
		commodities[c] = true
	}
	for c := range expenseTotal {
		commodities[c] = true
	}
	keys := make([]string, 0, len(commodities))
	for c := range commodities {
		keys = append(keys, c)
	}
	sort.Strings(keys)
	for _, c := range keys {
		net := incomeTotal[c]
		if net.Commodity == "" {
			net = model.Amount{Commodity: c}
		}
		if spent, ok := expenseTotal[c]; ok {
			if diff, err := net.Add(spent.Negate()); err == nil {
				net = diff
			}
		}
		stmt.Net = append(stmt.Net, net)
	}
	return stmt
}

// balanceSheet is the position on a date: what the books hold (Assets), what they owe
// (Liabilities), any Equity, and net worth (assets less what is owed). Like the income statement,
// amounts are per commodity, because cash and shares do not sum without a price.
type balanceSheet struct {
	AsOf        string
	Assets      []reportRow
	Liabilities []reportRow
	Equity      []reportRow
	Net         []model.Amount
}

// buildBalanceSheet folds every movement up to asOf (a zero time means all of it) into account
// balances, counting the source-account side too — the account the statement came from, which the
// entries elide — because that is where cash and debt actually sit. Liabilities and equity are
// credit-normal, so they are shown as the positive amount owed or held.
func buildBalanceSheet(txs []model.Transaction, entries []model.Entry, asOf time.Time, filter string, val *valuer) balanceSheet {
	raw := map[string]model.Amount{}
	// A holding is valued a posting at a time, because a balance's cost base is the sum of what each
	// acquisition cost, not one rate applied to the total quantity. So each posting is restated before
	// it accumulates, and only the accounts a balance sheet shows are folded — a foreign P&L amount is
	// not the sheet's to value, so it is left out rather than counted as unvalued.
	add := func(account string, amount model.Amount, cost *model.Amount) {
		if !matches(account, filter) {
			return
		}
		if !(isUnder(account, "Assets") || isUnder(account, "Liabilities") || isUnder(account, "Equity")) {
			return
		}
		valued := val.restate(amount, cost)
		addAmount(raw, account+"|"+valued.Commodity, valued)
	}
	for i, tx := range txs {
		if !asOf.IsZero() && tx.Date.After(asOf) {
			continue
		}
		add(entries[i].SourceAccount(tx), tx.Amount, nil)
		for _, p := range entries[i].Postings {
			add(p.Account, p.Amount, p.Cost)
		}
	}

	assets := map[string]model.Amount{}
	liabilities := map[string]model.Amount{}
	equity := map[string]model.Amount{}
	netTotal := map[string]model.Amount{}
	for key, amt := range raw {
		if amt.IsZero() {
			continue
		}
		account := key[:strings.LastIndex(key, "|")]
		switch {
		case isUnder(account, "Assets"):
			assets[key] = amt
			addAmount(netTotal, amt.Commodity, amt)
		case isUnder(account, "Liabilities"):
			liabilities[key] = amt.Negate() // credit-normal; show the positive amount owed
			addAmount(netTotal, amt.Commodity, amt)
		case isUnder(account, "Equity"):
			equity[key] = amt.Negate()
		}
	}

	sheet := balanceSheet{Assets: rowsOf(assets), Liabilities: rowsOf(liabilities), Equity: rowsOf(equity)}
	if !asOf.IsZero() {
		sheet.AsOf = asOf.Format("2006-01-02")
	}
	commodities := make([]string, 0, len(netTotal))
	for c := range netTotal {
		commodities = append(commodities, c)
	}
	sort.Strings(commodities)
	for _, c := range commodities {
		sheet.Net = append(sheet.Net, netTotal[c])
	}
	return sheet
}

// statementLine is one printed row of a statement section: a leaf account, or the Subtotal of a
// subsection that sums the leaves above it. A subsection is the level below a section -- "Real
// Estate" under "Expenses" -- and its subtotal lets a reader see what the grouping came to without
// adding the lines by hand.
type statementLine struct {
	Label    string // a leaf's account path, or "Total <subsection>" for a subtotal
	Amount   model.Amount
	Subtotal bool
}

// subsectionOf returns the subsection an account rolls up to: its first two colon-segments, e.g.
// "Expenses:Real Estate" for "Expenses:Real Estate:Interest". An account with only a section (or a
// bare name) is its own subsection.
func subsectionOf(account string) string {
	parts := strings.SplitN(account, ":", 3)
	if len(parts) < 2 {
		return account
	}
	return parts[0] + ":" + parts[1]
}

// withSubtotals turns sorted leaf rows into the lines a section prints: every leaf, and after each
// subsection that gathers more than one account, a per-commodity subtotal. Rows arrive sorted by
// account, so a subsection's leaves are contiguous. A subsection of a single account gets no
// subtotal -- it would only repeat the line -- and commodities are subtotaled apart, because a
// share and a dollar do not sum.
func withSubtotals(rows []reportRow) []statementLine {
	lines := make([]statementLine, 0, len(rows))
	for i := 0; i < len(rows); {
		sub := subsectionOf(rows[i].Account)
		accounts := map[string]bool{}
		totals := map[string]model.Amount{}
		var order []string
		j := i
		for j < len(rows) && subsectionOf(rows[j].Account) == sub {
			r := rows[j]
			lines = append(lines, statementLine{Label: r.Account, Amount: r.Amount})
			accounts[r.Account] = true
			if _, seen := totals[r.Amount.Commodity]; !seen {
				order = append(order, r.Amount.Commodity)
			}
			totals[r.Amount.Commodity] = plus(totals[r.Amount.Commodity], r.Amount)
			j++
		}
		if len(accounts) > 1 {
			label := sub
			if idx := strings.Index(sub, ":"); idx >= 0 {
				label = sub[idx+1:]
			}
			for _, c := range order {
				lines = append(lines, statementLine{Label: "Total " + label, Amount: totals[c], Subtotal: true})
			}
		}
		i = j
	}
	return lines
}

// matches reports whether an account is in scope: everything when filter is empty, else those whose
// path contains it.
func matches(account, filter string) bool {
	return filter == "" || strings.Contains(account, filter)
}

// isUnder reports whether an account is a top-level category or sits under it.
func isUnder(account, top string) bool {
	return account == top || strings.HasPrefix(account, top+":")
}

func inRange(d, from, to time.Time) bool {
	if !from.IsZero() && d.Before(from) {
		return false
	}
	if !to.IsZero() && d.After(to) {
		return false
	}
	return true
}

// addAmount sums an amount into a bucket keyed so every value in it shares a commodity, so Add never
// meets a mismatch.
func addAmount(m map[string]model.Amount, key string, a model.Amount) {
	if existing, ok := m[key]; ok {
		if sum, err := existing.Add(a); err == nil {
			m[key] = sum
			return
		}
	}
	m[key] = a
}

// rowsOf turns an account|commodity bucket into rows sorted by account then commodity.
func rowsOf(m map[string]model.Amount) []reportRow {
	rows := make([]reportRow, 0, len(m))
	for key, amt := range m {
		account := key[:strings.LastIndex(key, "|")]
		rows = append(rows, reportRow{Account: account, Amount: amt})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Account != rows[j].Account {
			return rows[i].Account < rows[j].Account
		}
		return rows[i].Amount.Commodity < rows[j].Amount.Commodity
	})
	return rows
}

// gainRow is one disposal on the capital-gains schedule: what left, what it raised, what it cost, and
// the realized gain (a loss is negative). Symbol is kept for sorting and the -account filter.
type gainRow struct {
	Date     string
	Symbol   string
	Quantity model.Amount // e.g. 10 AAPL
	Proceeds model.Amount
	Base     model.Amount
	Gain     model.Amount
}

// gainsSchedule is every disposal in a period with a total gain per commodity, the shape a tax
// return wants (Canada's Schedule 3, the T5008 world).
type gainsSchedule struct {
	From, To string
	Rows     []gainRow
	Total    []model.Amount
}

// capitalGains reads the resolved fold: a sale carries its shares leaving at their cost base and a
// gain posting, so each disposal's proceeds, base, and gain are already there, never recomputed. The
// realized gain is the negation of the income posting; proceeds is base plus gain. A line that
// disposes of several holdings at once splits the entry's gain across them by base, the last
// absorbing any rounding.
func capitalGains(txs []model.Transaction, entries []model.Entry, from, to time.Time, filter string) gainsSchedule {
	sched := gainsSchedule{}
	if !from.IsZero() {
		sched.From = from.Format("2006-01-02")
	}
	if !to.IsZero() {
		sched.To = to.Format("2006-01-02")
	}
	totals := map[string]model.Amount{}

	for i := range entries {
		tx, e := txs[i], entries[i]
		if e.Gain == "" || !inRange(tx.Date, from, to) {
			continue
		}

		var disposals []model.Posting
		var gain, baseTotal model.Amount
		for _, p := range e.Postings {
			switch {
			case p.Account == e.Gain:
				gain = plus(gain, p.Amount)
			case p.Cost != nil && p.Amount.Commodity != tx.Amount.Commodity && p.Amount.Units < 0:
				disposals = append(disposals, p)
				baseTotal = plus(baseTotal, *p.Cost)
			}
		}
		if len(disposals) == 0 {
			continue
		}
		gain = gain.Negate()

		assigned := model.Amount{Commodity: gain.Commodity}
		for j, d := range disposals {
			base := *d.Cost
			g := gain
			if len(disposals) > 1 {
				if j == len(disposals)-1 {
					g = plus(gain, assigned.Negate())
				} else if baseTotal.Units != 0 {
					g = model.Amount{Units: gain.Units * base.Units / baseTotal.Units, Scale: gain.Scale, Commodity: gain.Commodity}
					assigned = plus(assigned, g)
				}
			}
			if filter != "" && !strings.Contains(d.Account, filter) {
				continue
			}
			sched.Rows = append(sched.Rows, gainRow{
				Date:     tx.Date.Format("2006-01-02"),
				Symbol:   d.Amount.Commodity,
				Quantity: d.Amount.Negate(),
				Proceeds: plus(base, g),
				Base:     base,
				Gain:     g,
			})
			addAmount(totals, g.Commodity, g)
		}
	}

	sort.SliceStable(sched.Rows, func(i, j int) bool {
		if sched.Rows[i].Date != sched.Rows[j].Date {
			return sched.Rows[i].Date < sched.Rows[j].Date
		}
		return sched.Rows[i].Symbol < sched.Rows[j].Symbol
	})
	commodities := make([]string, 0, len(totals))
	for c := range totals {
		commodities = append(commodities, c)
	}
	sort.Strings(commodities)
	for _, c := range commodities {
		sched.Total = append(sched.Total, totals[c])
	}
	return sched
}

// plus adds two amounts of one commodity, treating a zero-value (no commodity) accumulator as empty.
func plus(a, b model.Amount) model.Amount {
	if a.Commodity == "" {
		return b
	}
	if sum, err := a.Add(b); err == nil {
		return sum
	}
	return a
}

// reportEntries folds the log for a report on the chosen basis. Cash reads only money that moved;
// accrual also books revenue earned and costs incurred before their cash, so an unpaid invoice shows
// on the day it was billed. It reuses the one basis engine (books.LedgerBasis) rather than re-deriving
// accrual, so a report can never drift from what `bkpr books -basis accrual` shows.
func reportEntries(log *eventlog.Log, accrual bool) ([]model.Transaction, []model.Entry, error) {
	basis := books.CashBasis
	if accrual {
		basis = books.AccrualBasis
	}
	return books.LedgerBasis(log, basis)
}

// reportView is what a render is handed: a statement is nil when the command did not build it.
type reportView struct {
	Income  *incomeStatement
	Balance *balanceSheet
	Gains   *gainsSchedule
}

// reportCmd dispatches to one statement (report income|balance|gains) or, with no subcommand, the
// combined income statement and balance sheet the README calls the company's "full picture".
// Each is its own command rather than a mode flag, so `report income` does exactly one thing.
func reportCmd(args []string) error {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "income":
			return reportIncomeCmd(args[1:])
		case "balance":
			return reportBalanceCmd(args[1:])
		case "gains":
			return reportGainsCmd(args[1:])
		default:
			return fmt.Errorf("unknown report subcommand %q; income, balance, or gains", args[0])
		}
	}
	return reportDefaultCmd(args)
}

// reportFlags is the shared surface every report command reads: what to narrow to (-account,
// -from, -to), how to read the log (-basis), how to render it (-format, -out), and the lens to
// value it through (-value, -rate).
type reportFlags struct {
	account  string
	from, to time.Time
	accrual  bool
	format   string
	val      *valuer
	out      string
}

// parseReportFlags parses and validates the flags common to report, report income, report balance,
// and report gains, so the four commands cannot drift out of sync on what a shared flag means.
func parseReportFlags(name string, args []string) (reportFlags, error) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	account := fs.String("account", "", "narrow to accounts whose path contains this text, e.g. a property, client, or symbol")
	fromStr := fs.String("from", "", "start date (inclusive), YYYY-MM-DD")
	toStr := fs.String("to", "", "end date (inclusive), YYYY-MM-DD")
	format := fs.String("format", "text", "text or html")
	basis := fs.String("basis", "cash", "cash or accrual: accrual recognizes invoices and bills when earned, before their cash")
	value := fs.String("value", "", "value foreign income and holdings in this commodity, e.g. CAD, using the @@ prices in the log")
	rate := fs.String("rate", "", "per-unit rates for amounts with no recorded price under -value, e.g. USD=1.35 (comma-separated)")
	out := fs.String("out", "", "write to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return reportFlags{}, err
	}
	if *basis != "cash" && *basis != "accrual" {
		return reportFlags{}, fmt.Errorf("-basis must be cash or accrual")
	}
	if *format != "text" && *format != "html" {
		return reportFlags{}, fmt.Errorf("-format must be text or html")
	}
	rates, err := parseRates(*rate, *value)
	if err != nil {
		return reportFlags{}, err
	}
	from, err := parseDay(*fromStr)
	if err != nil {
		return reportFlags{}, fmt.Errorf("-from: %w", err)
	}
	to, err := parseDay(*toStr)
	if err != nil {
		return reportFlags{}, fmt.Errorf("-to: %w", err)
	}
	return reportFlags{
		account: *account,
		from:    from,
		to:      to,
		accrual: *basis == "accrual",
		format:  *format,
		val:     newValuer(*value, rates),
		out:     *out,
	}, nil
}

// runReport owns the plumbing every report command shares: parse flags, fold the log on the chosen
// basis, hand build the transactions and entries to turn into a view, then render and write it. Each
// command supplies only build, which is the one thing it does.
func runReport(name string, args []string, build func(txs []model.Transaction, entries []model.Entry, f reportFlags) reportView) error {
	f, err := parseReportFlags(name, args)
	if err != nil {
		return err
	}
	s, err := store.OpenReader(".")
	if err != nil {
		return err
	}
	defer s.Close()

	txs, entries, err := reportEntries(s.Log, f.accrual)
	if err != nil {
		return err
	}
	view := build(txs, entries, f)
	warnUnvalued(f.val)

	render := renderReportText
	if f.format == "html" {
		render = renderReportHTML
	}
	return writeOut(f.out, func(w io.Writer) error { return render(w, view) })
}

// reportDefaultCmd is bare `report`: the income statement and balance sheet together, the "full
// picture" the README describes. Splitting the three statements into verbs should not cost this
// convenience, so it stays the no-subcommand default rather than needing a fourth verb of its own.
func reportDefaultCmd(args []string) error {
	return runReport("report", args, func(txs []model.Transaction, entries []model.Entry, f reportFlags) reportView {
		stmt := buildReport(txs, entries, f.from, f.to, f.account, f.val)
		// The balance sheet is a position as of the period's end (or all of it, if no end was given).
		sheet := buildBalanceSheet(txs, entries, f.to, f.account, f.val)
		return reportView{Income: &stmt, Balance: &sheet}
	})
}

// reportIncomeCmd is `report income`: what was earned and spent over the period, by account, with
// the net per commodity.
func reportIncomeCmd(args []string) error {
	return runReport("report income", args, func(txs []model.Transaction, entries []model.Entry, f reportFlags) reportView {
		stmt := buildReport(txs, entries, f.from, f.to, f.account, f.val)
		return reportView{Income: &stmt}
	})
}

// reportBalanceCmd is `report balance`: assets held and liabilities owed as of -to (or today), and
// net worth.
func reportBalanceCmd(args []string) error {
	return runReport("report balance", args, func(txs []model.Transaction, entries []model.Entry, f reportFlags) reportView {
		sheet := buildBalanceSheet(txs, entries, f.to, f.account, f.val)
		return reportView{Balance: &sheet}
	})
}

// reportGainsCmd is `report gains`: the tax-time view off the same cost-basis fold, a disposal per
// row with the total realized gain, for a filing (Canada's Schedule 3, the T5008 world).
func reportGainsCmd(args []string) error {
	return runReport("report gains", args, func(txs []model.Transaction, entries []model.Entry, f reportFlags) reportView {
		g := capitalGains(txs, entries, f.from, f.to, f.account)
		return reportView{Gains: &g}
	})
}

// warnUnvalued tells the reader, on stderr, which commodities the lens could not value and so left
// in their own currency — the recent USD income and USD cash that carry no @@ price. It points at the
// -rate flag that would convert them, so a total that looks foreign is understood, not mistaken for a
// gap in the books. It is silent with no lens or when everything valued.
func warnUnvalued(val *valuer) {
	if val == nil || val.target == "" || len(val.unpriced) == 0 {
		return
	}
	commodities := make([]string, 0, len(val.unpriced))
	for c := range val.unpriced {
		commodities = append(commodities, c)
	}
	sort.Strings(commodities)
	fmt.Fprintf(os.Stderr,
		"warning: %s had no recorded price to value in %s and is shown in its own currency; pass a rate to convert, e.g. -rate %s=1.35\n",
		strings.Join(commodities, ", "), val.target, commodities[0])
}

// writeOut sends a render to stdout, or to a file when path is set, reporting what it wrote. It is
// shared by report and invoice so the -out behaviour is identical.
func writeOut(path string, render func(io.Writer) error) error {
	if path == "" {
		return render(os.Stdout)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := render(f); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", path)
	return nil
}

// renderReportText writes the statements as plain tables, one after another with a blank line
// between whichever are shown.
func renderReportText(w io.Writer, v reportView) error {
	shown := 0
	sep := func() {
		if shown > 0 {
			fmt.Fprintln(w)
		}
		shown++
	}
	if v.Income != nil {
		sep()
		renderIncomeText(w, *v.Income)
	}
	if v.Balance != nil {
		sep()
		renderBalanceText(w, *v.Balance)
	}
	if v.Gains != nil {
		sep()
		renderGainsText(w, *v.Gains)
	}
	return nil
}

// renderGainsText writes the capital-gains schedule as a plain table: a disposal per row and the
// total gain in each commodity.
func renderGainsText(out io.Writer, s gainsSchedule) {
	fmt.Fprintf(out, "CAPITAL GAINS  (%s)\n\n", periodLabel(s.From, s.To))
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DATE\tDISPOSED\tPROCEEDS\tBASE\tGAIN")
	for _, r := range s.Rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Date, r.Quantity, r.Proceeds, r.Base, r.Gain)
	}
	if len(s.Rows) == 0 {
		fmt.Fprintln(w, "(no disposals in the period)")
	}
	w.Flush()
	for _, t := range s.Total {
		fmt.Fprintf(out, "Total gain: %s\n", t)
	}
}

func parseDay(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse("2006-01-02", s)
}

// renderIncomeText writes the income statement as a plain table: an income section, an expenses
// section, and the net in each commodity.
func renderIncomeText(out io.Writer, stmt incomeStatement) {
	fmt.Fprintf(out, "INCOME STATEMENT  (%s)\n\n", periodLabel(stmt.From, stmt.To))

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Income")
	writeRows(w, stmt.Income)
	fmt.Fprintln(w, "Expenses")
	writeRows(w, stmt.Expenses)
	fmt.Fprintln(w, "Net")
	for _, n := range stmt.Net {
		fmt.Fprintf(w, "\t%s\n", n)
	}
	w.Flush()
}

// writeRows prints a section's leaves and, beneath each multi-account subsection, its subtotal --
// the subtotal de-dented from the leaves so it reads as the summary of the group above it.
func writeRows(w *tabwriter.Writer, rows []reportRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}
	for _, ln := range withSubtotals(rows) {
		if ln.Subtotal {
			fmt.Fprintf(w, "  %s\t%s\n", ln.Label, ln.Amount)
		} else {
			fmt.Fprintf(w, "    %s\t%s\n", ln.Label, ln.Amount)
		}
	}
}

// renderBalanceText writes the position as a plain table: assets, liabilities and equity shown as
// positive amounts held or owed, and net worth in each commodity.
func renderBalanceText(out io.Writer, sheet balanceSheet) {
	fmt.Fprintf(out, "BALANCE SHEET  (%s)\n\n", asOfLabel(sheet.AsOf))

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Assets")
	writeRows(w, sheet.Assets)
	fmt.Fprintln(w, "Liabilities")
	writeRows(w, sheet.Liabilities)
	if len(sheet.Equity) > 0 {
		fmt.Fprintln(w, "Equity")
		writeRows(w, sheet.Equity)
	}
	fmt.Fprintln(w, "Net worth")
	for _, n := range sheet.Net {
		fmt.Fprintf(w, "\t%s\n", n)
	}
	w.Flush()
}

// periodLabel and asOfLabel describe the window a statement covers, shared by the text and HTML
// renderers so both read the same.
func periodLabel(from, to string) string {
	switch {
	case from != "" && to != "":
		return from + " .. " + to
	case from != "":
		return "from " + from
	case to != "":
		return "through " + to
	default:
		return "all dates"
	}
}

func asOfLabel(asOf string) string {
	if asOf == "" {
		return "now"
	}
	return "as of " + asOf
}

// reportTemplate renders the same statements as a self-contained HTML page, print-friendly like the
// invoice, so bkpr needs no PDF library.
var reportTemplate = template.Must(template.New("report").Parse(`<!doctype html>
<meta charset="utf-8">
<title>Report</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; color: #000; background: #fff; max-width: 42rem; margin: 2rem auto; padding: 0 1.5rem; line-height: 1.5; }
  h2 { font-size: 1.15rem; margin: 1.5rem 0 0.1rem; }
  .period { color: #666; font-size: 0.85rem; margin-bottom: 0.5rem; }
  h3 { font-size: 0.8rem; text-transform: uppercase; opacity: 0.6; margin: 1rem 0 0.1rem; }
  table { width: 100%; border-collapse: collapse; }
  td { padding: 0.15rem 0.25rem; }
  th { padding: 0.15rem 0.25rem; text-align: left; text-transform: uppercase; font-size: 0.7rem; opacity: 0.6; border-bottom: 1px solid #ccc; }
  th.amt, td.amt { text-align: right; }
  .amt { text-align: right; font-variant-numeric: tabular-nums; }
  .total td { border-top: 2px solid #000; font-weight: bold; }
  .subtotal td { font-weight: bold; }
  .subtotal td:first-child { border-top: 1px solid #ccc; }
  .none { color: #999; }
  @media print { body { margin: 0; } }
</style>
{{if .Income}}
<h2>Income statement</h2>
<div class="period">{{.IncomePeriod}}</div>
<h3>Income</h3>
<table>{{range .IncomeLines}}<tr{{if .Subtotal}} class="subtotal"{{end}}><td>{{.Label}}</td><td class="amt">{{.Amount}}</td></tr>{{else}}<tr><td class="none">(none)</td><td></td></tr>{{end}}</table>
<h3>Expenses</h3>
<table>{{range .ExpenseLines}}<tr{{if .Subtotal}} class="subtotal"{{end}}><td>{{.Label}}</td><td class="amt">{{.Amount}}</td></tr>{{else}}<tr><td class="none">(none)</td><td></td></tr>{{end}}</table>
<table>{{range .Income.Net}}<tr class="total"><td>Net</td><td class="amt">{{.}}</td></tr>{{end}}</table>
{{end}}
{{if .Balance}}
<h2>Balance sheet</h2>
<div class="period">{{.BalanceAsOf}}</div>
<h3>Assets</h3>
<table>{{range .AssetLines}}<tr{{if .Subtotal}} class="subtotal"{{end}}><td>{{.Label}}</td><td class="amt">{{.Amount}}</td></tr>{{else}}<tr><td class="none">(none)</td><td></td></tr>{{end}}</table>
<h3>Liabilities</h3>
<table>{{range .LiabilityLines}}<tr{{if .Subtotal}} class="subtotal"{{end}}><td>{{.Label}}</td><td class="amt">{{.Amount}}</td></tr>{{else}}<tr><td class="none">(none)</td><td></td></tr>{{end}}</table>
{{if .EquityLines}}<h3>Equity</h3>
<table>{{range .EquityLines}}<tr{{if .Subtotal}} class="subtotal"{{end}}><td>{{.Label}}</td><td class="amt">{{.Amount}}</td></tr>{{end}}</table>{{end}}
<table>{{range .Balance.Net}}<tr class="total"><td>Net worth</td><td class="amt">{{.}}</td></tr>{{end}}</table>
{{end}}
{{if .Gains}}
<h2>Capital gains</h2>
<div class="period">{{.GainsPeriod}}</div>
<table>
  <tr><th>Date</th><th>Disposed</th><th class="amt">Proceeds</th><th class="amt">Base</th><th class="amt">Gain</th></tr>
  {{range .Gains.Rows}}<tr><td>{{.Date}}</td><td>{{.Quantity}}</td><td class="amt">{{.Proceeds}}</td><td class="amt">{{.Base}}</td><td class="amt">{{.Gain}}</td></tr>{{else}}<tr><td class="none">(no disposals)</td><td></td><td></td><td></td><td></td></tr>{{end}}
  {{range .Gains.Total}}<tr class="total"><td>Total gain</td><td></td><td></td><td></td><td class="amt">{{.}}</td></tr>{{end}}
</table>
{{end}}
`))

// renderReportHTML renders the statements as one HTML page.
func renderReportHTML(w io.Writer, v reportView) error {
	data := struct {
		Income         *incomeStatement
		IncomePeriod   string
		IncomeLines    []statementLine
		ExpenseLines   []statementLine
		Balance        *balanceSheet
		BalanceAsOf    string
		AssetLines     []statementLine
		LiabilityLines []statementLine
		EquityLines    []statementLine
		Gains          *gainsSchedule
		GainsPeriod    string
	}{Income: v.Income, Balance: v.Balance, Gains: v.Gains}
	if v.Income != nil {
		data.IncomePeriod = periodLabel(v.Income.From, v.Income.To)
		data.IncomeLines = withSubtotals(v.Income.Income)
		data.ExpenseLines = withSubtotals(v.Income.Expenses)
	}
	if v.Balance != nil {
		data.BalanceAsOf = asOfLabel(v.Balance.AsOf)
		data.AssetLines = withSubtotals(v.Balance.Assets)
		data.LiabilityLines = withSubtotals(v.Balance.Liabilities)
		data.EquityLines = withSubtotals(v.Balance.Equity)
	}
	if v.Gains != nil {
		data.GainsPeriod = periodLabel(v.Gains.From, v.Gains.To)
	}
	return reportTemplate.Execute(w, data)
}
