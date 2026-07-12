package main

import (
	"flag"
	"fmt"
	"html/template"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
	"github.com/dallasread/bookkeeper/lib/store"
)

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
func buildReport(txs []model.Transaction, entries []model.Entry, from, to time.Time, filter string) incomeStatement {
	incomeRows := map[string]model.Amount{}
	expenseRows := map[string]model.Amount{}
	incomeTotal := map[string]model.Amount{}
	expenseTotal := map[string]model.Amount{}

	fold := func(account string, amount model.Amount) {
		if !matches(account, filter) {
			return
		}
		switch {
		case isUnder(account, "Income"):
			earned := amount.Negate() // income is negative-normal; a statement shows it positive
			addAmount(incomeRows, account+"|"+earned.Commodity, earned)
			addAmount(incomeTotal, earned.Commodity, earned)
		case isUnder(account, "Expenses"):
			addAmount(expenseRows, account+"|"+amount.Commodity, amount)
			addAmount(expenseTotal, amount.Commodity, amount)
		}
	}

	for i, tx := range txs {
		if !inRange(tx.Date, from, to) {
			continue
		}
		fold(tx.Account, tx.Amount)
		for _, p := range entries[i].Postings {
			fold(p.Account, p.Amount)
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
func buildBalanceSheet(txs []model.Transaction, entries []model.Entry, asOf time.Time, filter string) balanceSheet {
	raw := map[string]model.Amount{}
	for i, tx := range txs {
		if !asOf.IsZero() && tx.Date.After(asOf) {
			continue
		}
		if matches(tx.Account, filter) {
			addAmount(raw, tx.Account+"|"+tx.Amount.Commodity, tx.Amount)
		}
		for _, p := range entries[i].Postings {
			if matches(p.Account, filter) {
				addAmount(raw, p.Account+"|"+p.Amount.Commodity, p.Amount)
			}
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
// accrual, so a report can never drift from what `bk books -basis accrual` shows.
func reportEntries(log *eventlog.Log, accrual bool) ([]model.Transaction, []model.Entry, error) {
	basis := books.CashBasis
	if accrual {
		basis = books.AccrualBasis
	}
	return books.LedgerBasis(log, basis)
}

// reportView is what a render is handed: a statement is nil when a flag narrowed it out.
type reportView struct {
	Income  *incomeStatement
	Balance *balanceSheet
	Gains   *gainsSchedule
}

// reportCmd renders the full picture of the books: an income statement over the period and a balance
// sheet as of its end. -income, -balance, or -gains narrows to one; -account narrows to a property,
// client, or symbol; -basis reads it on cash (the default) or accrual; -format picks text (the
// default) or html; -out writes to a file.
func reportCmd(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	account := fs.String("account", "", "narrow to accounts whose path contains this text, e.g. a property, client, or symbol")
	fromStr := fs.String("from", "", "start date (inclusive), YYYY-MM-DD")
	toStr := fs.String("to", "", "end date (inclusive), YYYY-MM-DD")
	incomeOnly := fs.Bool("income", false, "show only the income statement")
	balanceOnly := fs.Bool("balance", false, "show only the balance sheet")
	gainsOnly := fs.Bool("gains", false, "show only the capital-gains schedule")
	format := fs.String("format", "text", "text or html")
	basis := fs.String("basis", "cash", "cash or accrual: accrual recognizes invoices and bills when earned, before their cash")
	out := fs.String("out", "", "write to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *basis != "cash" && *basis != "accrual" {
		return fmt.Errorf("-basis must be cash or accrual")
	}
	only := 0
	for _, b := range []bool{*incomeOnly, *balanceOnly, *gainsOnly} {
		if b {
			only++
		}
	}
	if only > 1 {
		return fmt.Errorf("give one of -income, -balance, or -gains")
	}
	if *format != "text" && *format != "html" {
		return fmt.Errorf("-format must be text or html")
	}
	from, err := parseDay(*fromStr)
	if err != nil {
		return fmt.Errorf("-from: %w", err)
	}
	to, err := parseDay(*toStr)
	if err != nil {
		return fmt.Errorf("-to: %w", err)
	}

	s, err := store.Open(".")
	if err != nil {
		return err
	}
	defer s.Close()

	txs, entries, err := reportEntries(s.Log, *basis == "accrual")
	if err != nil {
		return err
	}

	var view reportView
	if *gainsOnly {
		g := capitalGains(txs, entries, from, to, *account)
		view.Gains = &g
	} else {
		if !*balanceOnly {
			stmt := buildReport(txs, entries, from, to, *account)
			view.Income = &stmt
		}
		if !*incomeOnly {
			// The balance sheet is a position as of the period's end (or all of it, if no end was given).
			sheet := buildBalanceSheet(txs, entries, to, *account)
			view.Balance = &sheet
		}
	}

	render := renderReportText
	if *format == "html" {
		render = renderReportHTML
	}
	return writeOut(*out, func(w io.Writer) error { return render(w, view) })
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

func writeRows(w *tabwriter.Writer, rows []reportRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}
	for _, r := range rows {
		fmt.Fprintf(w, "  %s\t%s\n", r.Account, r.Amount)
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
// invoice, so bookkeeper needs no PDF library.
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
  .none { color: #999; }
  @media print { body { margin: 0; } }
</style>
{{if .Income}}
<h2>Income statement</h2>
<div class="period">{{.IncomePeriod}}</div>
<h3>Income</h3>
<table>{{range .Income.Income}}<tr><td>{{.Account}}</td><td class="amt">{{.Amount}}</td></tr>{{else}}<tr><td class="none">(none)</td><td></td></tr>{{end}}</table>
<h3>Expenses</h3>
<table>{{range .Income.Expenses}}<tr><td>{{.Account}}</td><td class="amt">{{.Amount}}</td></tr>{{else}}<tr><td class="none">(none)</td><td></td></tr>{{end}}</table>
<table>{{range .Income.Net}}<tr class="total"><td>Net</td><td class="amt">{{.}}</td></tr>{{end}}</table>
{{end}}
{{if .Balance}}
<h2>Balance sheet</h2>
<div class="period">{{.BalanceAsOf}}</div>
<h3>Assets</h3>
<table>{{range .Balance.Assets}}<tr><td>{{.Account}}</td><td class="amt">{{.Amount}}</td></tr>{{else}}<tr><td class="none">(none)</td><td></td></tr>{{end}}</table>
<h3>Liabilities</h3>
<table>{{range .Balance.Liabilities}}<tr><td>{{.Account}}</td><td class="amt">{{.Amount}}</td></tr>{{else}}<tr><td class="none">(none)</td><td></td></tr>{{end}}</table>
{{if .Balance.Equity}}<h3>Equity</h3>
<table>{{range .Balance.Equity}}<tr><td>{{.Account}}</td><td class="amt">{{.Amount}}</td></tr>{{end}}</table>{{end}}
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
		Income       *incomeStatement
		IncomePeriod string
		Balance      *balanceSheet
		BalanceAsOf  string
		Gains        *gainsSchedule
		GainsPeriod  string
	}{Income: v.Income, Balance: v.Balance, Gains: v.Gains}
	if v.Income != nil {
		data.IncomePeriod = periodLabel(v.Income.From, v.Income.To)
	}
	if v.Balance != nil {
		data.BalanceAsOf = asOfLabel(v.Balance.AsOf)
	}
	if v.Gains != nil {
		data.GainsPeriod = periodLabel(v.Gains.From, v.Gains.To)
	}
	return reportTemplate.Execute(w, data)
}
