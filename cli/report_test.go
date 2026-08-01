package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bkpr.pro/bkpr/lib/books"
	"bkpr.pro/bkpr/lib/eventlog"
	"bkpr.pro/bkpr/lib/model"
	"bkpr.pro/bkpr/lib/store"
)

func cad2(cents int64) model.Amount { return model.Amount{Units: cents, Scale: 2, Commodity: "CAD"} }

// entry pairs a date with its postings; buildReport reads only those two.
func entryOn(day int, postings ...model.Posting) (model.Transaction, model.Entry) {
	return model.Transaction{Date: on(day)}, model.Entry{Postings: postings}
}

func post(account string, a model.Amount) model.Posting {
	return model.Posting{Account: account, Amount: a}
}

func rowAmount(t *testing.T, rows []reportRow, account string) string {
	t.Helper()
	for _, r := range rows {
		if r.Account == account {
			return r.Amount.String()
		}
	}
	t.Fatalf("no row for %q in %+v", account, rows)
	return ""
}

// Income shows positive (it is negative-normal in the books), expenses show as spent, and the net is
// income less expenses.
func TestReportGroupsIncomeAndExpensesAndNets(t *testing.T) {
	tx1, e1 := entryOn(1, post("Income:Rent:123 Main", cad2(-160000)))
	tx2, e2 := entryOn(2, post("Expenses:Fuel", cad2(6240)))
	stmt := buildReport([]model.Transaction{tx1, tx2}, []model.Entry{e1, e2}, time.Time{}, time.Time{}, "", nil)

	if got := rowAmount(t, stmt.Income, "Income:Rent:123 Main"); got != "1600.00 CAD" {
		t.Errorf("income = %q, want it shown positive", got)
	}
	if got := rowAmount(t, stmt.Expenses, "Expenses:Fuel"); got != "62.40 CAD" {
		t.Errorf("expense = %q", got)
	}
	if len(stmt.Net) != 1 || stmt.Net[0].String() != "1537.60 CAD" {
		t.Errorf("net = %+v, want 1537.60 CAD", stmt.Net)
	}
}

// A USD fee and CAD rent do not sum, so the net is reported in each commodity on its own.
func TestReportNetsPerCommodity(t *testing.T) {
	tx1, e1 := entryOn(1, post("Income:Rent", cad2(-100000)))
	tx2, e2 := entryOn(2, post("Income:Consulting:Acme", model.Amount{Units: -1000, Commodity: "USD"}))
	stmt := buildReport([]model.Transaction{tx1, tx2}, []model.Entry{e1, e2}, time.Time{}, time.Time{}, "", nil)

	if len(stmt.Net) != 2 {
		t.Fatalf("net = %+v, want one per commodity", stmt.Net)
	}
	if stmt.Net[0].String() != "1000.00 CAD" || stmt.Net[1].String() != "1000 USD" {
		t.Errorf("net = %+v, want CAD then USD", stmt.Net)
	}
}

// A substring filter reaches a property across both trees: its rent income and its repair expense.
func TestReportFiltersByAccountSubstring(t *testing.T) {
	tx1, e1 := entryOn(1, post("Income:Rent:123 Main", cad2(-160000)))
	tx2, e2 := entryOn(2, post("Expenses:Repairs:123 Main", cad2(4000)))
	tx3, e3 := entryOn(3, post("Income:Rent:45 Elm", cad2(-90000)))
	stmt := buildReport([]model.Transaction{tx1, tx2, tx3}, []model.Entry{e1, e2, e3}, time.Time{}, time.Time{}, "123 Main", nil)

	if len(stmt.Income) != 1 || stmt.Income[0].Account != "Income:Rent:123 Main" {
		t.Errorf("income = %+v, want only the filtered property", stmt.Income)
	}
	if len(stmt.Expenses) != 1 || stmt.Expenses[0].Account != "Expenses:Repairs:123 Main" {
		t.Errorf("expenses = %+v, want the property's repair", stmt.Expenses)
	}
}

// A hand-kept entry can put its expense or income leg last, where the importer reads it as the
// line's own account. The ledger artifact and ledger-cli count both sides, so the income
// statement must match them.
func TestReportCountsAnIncomeOrExpenseSourceAccount(t *testing.T) {
	tx1, e1 := bs(1, "Expenses:Real Estate:Interest", cad2(151174), post("Assets:Bank:Chequing", cad2(-151174)))
	tx2, e2 := bs(2, "Income:Rent:123 Main", cad2(-160000), post("Assets:Bank:Chequing", cad2(160000)))
	stmt := buildReport([]model.Transaction{tx1, tx2}, []model.Entry{e1, e2}, time.Time{}, time.Time{}, "", nil)

	if got := rowAmount(t, stmt.Expenses, "Expenses:Real Estate:Interest"); got != "1511.74 CAD" {
		t.Errorf("expense = %q, want the line's own account counted", got)
	}
	if got := rowAmount(t, stmt.Income, "Income:Rent:123 Main"); got != "1600.00 CAD" {
		t.Errorf("income = %q, want it shown positive", got)
	}
	if len(stmt.Net) != 1 || stmt.Net[0].String() != "88.26 CAD" {
		t.Errorf("net = %+v, want 88.26 CAD", stmt.Net)
	}
}

// The -account filter reaches a source-account expense the same way it reaches a posting.
func TestReportFilterReachesTheSourceAccount(t *testing.T) {
	tx1, e1 := bs(1, "Expenses:Repairs:123 Main", cad2(4000), post("Assets:Bank:Chequing", cad2(-4000)))
	tx2, e2 := bs(2, "Expenses:Fuel", cad2(6240), post("Assets:Bank:Chequing", cad2(-6240)))
	stmt := buildReport([]model.Transaction{tx1, tx2}, []model.Entry{e1, e2}, time.Time{}, time.Time{}, "123 Main", nil)

	if len(stmt.Expenses) != 1 || stmt.Expenses[0].Account != "Expenses:Repairs:123 Main" {
		t.Errorf("expenses = %+v, want only the filtered property's", stmt.Expenses)
	}
}

// bs pairs a source account and amount with a date and categorized postings: the two sides every
// statement folds.
func bs(day int, account string, amount model.Amount, postings ...model.Posting) (model.Transaction, model.Entry) {
	return model.Transaction{Date: on(day), Account: account, Amount: amount}, model.Entry{Postings: postings}
}

// The balance sheet holds what the source accounts net to, and owes liabilities as a positive
// amount; net worth is assets less what is owed.
func TestBalanceSheetHoldsAssetsAndOwesLiabilities(t *testing.T) {
	tx1, e1 := bs(1, "Assets:Bank:Chequing", cad2(160000), post("Income:Rent", cad2(-160000)))
	tx2, e2 := bs(2, "Assets:Bank:Chequing", cad2(-6240), post("Expenses:Fuel", cad2(6240)))
	tx3, e3 := bs(3, "Liabilities:Card:Visa", cad2(-20000), post("Expenses:Misc", cad2(20000)))
	sheet := buildBalanceSheet([]model.Transaction{tx1, tx2, tx3}, []model.Entry{e1, e2, e3}, time.Time{}, "", nil)

	if got := rowAmount(t, sheet.Assets, "Assets:Bank:Chequing"); got != "1537.60 CAD" {
		t.Errorf("chequing = %q", got)
	}
	if got := rowAmount(t, sheet.Liabilities, "Liabilities:Card:Visa"); got != "200.00 CAD" {
		t.Errorf("card = %q, want the positive amount owed", got)
	}
	if len(sheet.Net) != 1 || sheet.Net[0].String() != "1337.60 CAD" {
		t.Errorf("net worth = %+v, want 1337.60 CAD", sheet.Net)
	}
}

// A routed entry's source leg lands where it was routed, so the balance sheet agrees with the
// account list and reconcile: a card charge registered on the parent but routed to a purpose child
// owes on the child, and the parent holds nothing of it directly.
func TestBalanceSheetPutsARoutedLegOnTheChild(t *testing.T) {
	tx := model.Transaction{Date: on(1), Account: "Liabilities:PC Mastercard", Amount: cad2(-10000)}
	e := model.Entry{Source: "Liabilities:PC Mastercard:9 Schoodic", Postings: []model.Posting{post("Expenses:Materials:9 Schoodic", cad2(10000))}}
	sheet := buildBalanceSheet([]model.Transaction{tx}, []model.Entry{e}, time.Time{}, "", nil)

	if got := rowAmount(t, sheet.Liabilities, "Liabilities:PC Mastercard:9 Schoodic"); got != "100.00 CAD" {
		t.Errorf("routed child owes %q, want 100.00 CAD", got)
	}
	for _, r := range sheet.Liabilities {
		if r.Account == "Liabilities:PC Mastercard" {
			t.Errorf("the registered parent shows %s directly, want none: the leg routed to the child", r.Amount)
		}
	}
}

// Shares sit on the balance sheet in their own commodity, not as a dollar figure.
func TestBalanceSheetHoldsShares(t *testing.T) {
	cost := model.Amount{Units: 100000, Scale: 2, Commodity: "USD"}
	tx, e := bs(1, "Assets:Brokerage:Cash", model.Amount{Units: -100000, Scale: 2, Commodity: "USD"},
		model.Posting{Account: "Assets:Brokerage:AAPL", Amount: model.Amount{Units: 10, Commodity: "AAPL"}, Cost: &cost})
	sheet := buildBalanceSheet([]model.Transaction{tx}, []model.Entry{e}, time.Time{}, "", nil)

	if got := rowAmount(t, sheet.Assets, "Assets:Brokerage:AAPL"); got != "10 AAPL" {
		t.Errorf("shares = %q, want 10 AAPL", got)
	}
}

// Income and expense accounts are the P&L, so they never appear on the balance sheet.
func TestBalanceSheetExcludesIncomeAndExpenses(t *testing.T) {
	tx, e := bs(1, "Assets:Bank:Chequing", cad2(160000), post("Income:Rent", cad2(-160000)))
	sheet := buildBalanceSheet([]model.Transaction{tx}, []model.Entry{e}, time.Time{}, "", nil)

	all := []reportRow{}
	all = append(all, sheet.Assets...)
	all = append(all, sheet.Liabilities...)
	all = append(all, sheet.Equity...)
	for _, r := range all {
		if isUnder(r.Account, "Income") || isUnder(r.Account, "Expenses") {
			t.Errorf("balance sheet carries a P&L account: %s", r.Account)
		}
	}
}

// The position is as of a date: a later movement is not yet on it.
func TestBalanceSheetRespectsAsOf(t *testing.T) {
	tx1, e1 := bs(2, "Assets:Bank:Chequing", cad2(100000), post("Income:Rent", cad2(-100000)))
	tx2, e2 := bs(20, "Assets:Bank:Chequing", cad2(50000), post("Income:Rent", cad2(-50000)))
	asOf := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	sheet := buildBalanceSheet([]model.Transaction{tx1, tx2}, []model.Entry{e1, e2}, asOf, "", nil)

	if got := rowAmount(t, sheet.Assets, "Assets:Bank:Chequing"); got != "1000.00 CAD" {
		t.Errorf("chequing as of = %q, want only the in-range 1000.00 CAD", got)
	}
}

// sale builds a resolved sale entry the way the cost-basis fold leaves it: the shares out at their
// base, and the gain posting. proceedsCents and baseCents are in the sale's currency.
func sale(day int, symbol string, qty int64, baseCents, gainCents int64) (model.Transaction, model.Entry) {
	cost := usd(baseCents)
	tx := model.Transaction{Date: on(day), Account: "Assets:Brokerage:Cash", Amount: usd(baseCents + gainCents)}
	entry := model.Entry{Gain: "Income:Capital Gains", Postings: []model.Posting{
		{Account: "Assets:Brokerage:" + symbol, Amount: model.Amount{Units: -qty, Commodity: symbol}, Cost: &cost},
		{Account: "Income:Capital Gains", Amount: usd(-gainCents)},
	}}
	return tx, entry
}

// Each disposal is a row with proceeds, base, and gain read straight off the fold; the schedule totals
// the gain.
func TestCapitalGainsRowPerDisposal(t *testing.T) {
	tx, e := sale(15, "AAPL", 10, 100000, 20000) // base 1000, gain 200, proceeds 1200
	s := capitalGains([]model.Transaction{tx}, []model.Entry{e}, time.Time{}, time.Time{}, "")

	if len(s.Rows) != 1 {
		t.Fatalf("rows = %+v", s.Rows)
	}
	r := s.Rows[0]
	if r.Symbol != "AAPL" || r.Quantity.String() != "10 AAPL" {
		t.Errorf("disposed = %+v", r)
	}
	if r.Base.String() != "1000.00 USD" || r.Proceeds.String() != "1200.00 USD" || r.Gain.String() != "200.00 USD" {
		t.Errorf("base=%s proceeds=%s gain=%s", r.Base, r.Proceeds, r.Gain)
	}
	if len(s.Total) != 1 || s.Total[0].String() != "200.00 USD" {
		t.Errorf("total = %+v", s.Total)
	}
}

// A disposal below its base is a loss: the gain is negative.
func TestCapitalGainsShowsALoss(t *testing.T) {
	tx, e := sale(15, "AAPL", 10, 100000, -20000) // base 1000, gain -200, proceeds 800
	s := capitalGains([]model.Transaction{tx}, []model.Entry{e}, time.Time{}, time.Time{}, "")
	if s.Rows[0].Gain.String() != "-200.00 USD" || s.Rows[0].Proceeds.String() != "800.00 USD" {
		t.Errorf("row = %+v", s.Rows[0])
	}
	if s.Total[0].String() != "-200.00 USD" {
		t.Errorf("total = %+v", s.Total)
	}
}

// Only disposals in the period are on the schedule, so a tax year is bounded by -from/-to.
func TestCapitalGainsFiltersByDate(t *testing.T) {
	tx1, e1 := sale(2, "AAPL", 10, 100000, 20000)
	tx2, e2 := sale(20, "MSFT", 5, 50000, 10000)
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	s := capitalGains([]model.Transaction{tx1, tx2}, []model.Entry{e1, e2}, from, to, "")
	if len(s.Rows) != 1 || s.Rows[0].Symbol != "AAPL" {
		t.Errorf("rows = %+v, want only the in-range AAPL disposal", s.Rows)
	}
}

// A non-sale entry (no gain account) contributes nothing.
func TestCapitalGainsIgnoresNonSales(t *testing.T) {
	tx, e := entryOn(1, post("Expenses:Fuel", cad2(6240)))
	s := capitalGains([]model.Transaction{tx}, []model.Entry{e}, time.Time{}, time.Time{}, "")
	if len(s.Rows) != 0 {
		t.Errorf("rows = %+v, want none", s.Rows)
	}
}

func usd2(cents int64) model.Amount { return model.Amount{Units: cents, Scale: 2, Commodity: "USD"} }

// priced pairs a foreign amount with a @@ cost in another commodity, the shape the DNSimple income
// lines take: -9000 USD @@ 12157.12 CAD.
func priced(account string, a, cost model.Amount) model.Posting {
	return model.Posting{Account: account, Amount: a, Cost: &cost}
}

// Under a -value CAD lens, USD income that recorded its own @@ CAD price is read at that exact price,
// not at its USD face. This is the whole ask: DNSimple's -9000 USD @@ 12157.12 CAD shows as the CAD
// the books already kept for it.
func TestReportValuesIncomeAtItsRecordedPrice(t *testing.T) {
	tx, e := entryOn(1, priced("Income:Consulting:DNSimple", usd2(-900000), cad2(1215712)))
	val := newValuer("CAD", nil)
	stmt := buildReport([]model.Transaction{tx}, []model.Entry{e}, time.Time{}, time.Time{}, "", val)

	if got := rowAmount(t, stmt.Income, "Income:Consulting:DNSimple"); got != "12157.12 CAD" {
		t.Errorf("income = %q, want it valued at the recorded @@ CAD price", got)
	}
	if len(stmt.Net) != 1 || stmt.Net[0].String() != "12157.12 CAD" {
		t.Errorf("net = %+v, want a single CAD figure", stmt.Net)
	}
	if len(val.unpriced) != 0 {
		t.Errorf("unpriced = %+v, want none: the line carried its own price", val.unpriced)
	}
}

// Foreign income with no recorded price is left in its own currency, and its commodity is remembered
// so the command can warn. This is the recent USD-billed income that landed as USD.
func TestReportLeavesUnpricedForeignIncomeNative(t *testing.T) {
	tx, e := entryOn(1, post("Income:Consulting:DNSimple", usd2(-900000)))
	val := newValuer("CAD", nil)
	stmt := buildReport([]model.Transaction{tx}, []model.Entry{e}, time.Time{}, time.Time{}, "", val)

	if got := rowAmount(t, stmt.Income, "Income:Consulting:DNSimple"); got != "9000.00 USD" {
		t.Errorf("income = %q, want it left in USD with no price to value it", got)
	}
	if !val.unpriced["USD"] {
		t.Errorf("unpriced = %+v, want USD remembered so the reader is warned", val.unpriced)
	}
}

// A -rate values the unpriced residual: one USD is worth 1.35 CAD, so 9000 USD reads as 12150.00 CAD.
func TestReportValuesUnpricedForeignIncomeAtASuppliedRate(t *testing.T) {
	tx, e := entryOn(1, post("Income:Consulting:DNSimple", usd2(-900000)))
	val := newValuer("CAD", map[string]model.Amount{"USD": cad2(135)})
	stmt := buildReport([]model.Transaction{tx}, []model.Entry{e}, time.Time{}, time.Time{}, "", val)

	if got := rowAmount(t, stmt.Income, "Income:Consulting:DNSimple"); got != "12150.00 CAD" {
		t.Errorf("income = %q, want 9000 USD at 1.35 = 12150.00 CAD", got)
	}
	if len(val.unpriced) != 0 {
		t.Errorf("unpriced = %+v, want none: the rate covered it", val.unpriced)
	}
}

// A holding is valued at the sum of what each acquisition cost, not one rate on the total quantity: a
// Door bought for 99000 CAD and another for 45500 CAD sits at 144500.00 CAD.
func TestBalanceSheetValuesAHoldingAtItsCostBasis(t *testing.T) {
	door := func(day int, costCents int64) (model.Transaction, model.Entry) {
		return model.Transaction{Date: on(day), Account: "Equity:Real Estate", Amount: model.Amount{Units: -1, Commodity: "Doors"}},
			model.Entry{Postings: []model.Posting{priced("Assets:Real Estate", model.Amount{Units: 1, Commodity: "Doors"}, cad2(costCents))}}
	}
	tx1, e1 := door(1, 9900000)
	tx2, e2 := door(2, 4550000)
	val := newValuer("CAD", nil)
	sheet := buildBalanceSheet([]model.Transaction{tx1, tx2}, []model.Entry{e1, e2}, time.Time{}, "", val)

	if got := rowAmount(t, sheet.Assets, "Assets:Real Estate"); got != "144500.00 CAD" {
		t.Errorf("holding = %q, want the summed cost basis", got)
	}
}

// A foreign cash balance with no recorded price stays in its own currency and is flagged, the way the
// 37000 USD chequing balance does.
func TestBalanceSheetLeavesUnpricedForeignCashNative(t *testing.T) {
	tx, e := bs(1, "Assets:Bank:USD", usd2(3700000), post("Income:Consulting", usd2(-3700000)))
	val := newValuer("CAD", nil)
	sheet := buildBalanceSheet([]model.Transaction{tx}, []model.Entry{e}, time.Time{}, "", val)

	if got := rowAmount(t, sheet.Assets, "Assets:Bank:USD"); got != "37000.00 USD" {
		t.Errorf("cash = %q, want it left in USD", got)
	}
	if !val.unpriced["USD"] {
		t.Errorf("unpriced = %+v, want the foreign asset remembered; the foreign P&L side must not add noise", val.unpriced)
	}
}

// With no lens (a nil valuer) the report is unchanged: amounts keep their own commodity.
func TestReportWithoutLensIsUnchanged(t *testing.T) {
	tx, e := entryOn(1, priced("Income:Consulting:DNSimple", usd2(-900000), cad2(1215712)))
	stmt := buildReport([]model.Transaction{tx}, []model.Entry{e}, time.Time{}, time.Time{}, "", nil)
	if got := rowAmount(t, stmt.Income, "Income:Consulting:DNSimple"); got != "9000.00 USD" {
		t.Errorf("income = %q, want the untouched USD face without a lens", got)
	}
}

// -rate is parsed into a per-unit price in the target, refuses a target-less rate, and refuses
// pricing the target against itself.
func TestParseRates(t *testing.T) {
	rates, err := parseRates("USD=1.35", "CAD")
	if err != nil {
		t.Fatalf("parseRates: %v", err)
	}
	if got := rates["USD"].String(); got != "1.35 CAD" {
		t.Errorf("USD rate = %q, want 1.35 CAD", got)
	}
	if _, err := parseRates("USD=1.35", ""); err == nil {
		t.Errorf("a rate with no -value should be refused")
	}
	if _, err := parseRates("CAD=1", "CAD"); err == nil {
		t.Errorf("pricing the target in itself should be refused")
	}
	if _, err := parseRates("USD", "CAD"); err == nil {
		t.Errorf("a rate without = should be refused")
	}
}

// A subsection that gathers more than one account is subtotaled, so a reader sees what the grouping
// came to without adding the lines by hand. The subtotal is labelled by the subsection and sums the
// leaves above it.
func TestWithSubtotalsSumsMultiAccountSubsections(t *testing.T) {
	rows := []reportRow{
		{Account: "Expenses:Fuel", Amount: cad2(6240)},
		{Account: "Expenses:Real Estate:Insurance", Amount: cad2(20000)},
		{Account: "Expenses:Real Estate:Interest", Amount: cad2(151174)},
	}
	lines := withSubtotals(rows)

	var sub *statementLine
	for i := range lines {
		if lines[i].Subtotal {
			if sub != nil {
				t.Fatalf("want a single subtotal, got another: %+v", lines[i])
			}
			sub = &lines[i]
		}
	}
	if sub == nil || sub.Label != "Total Real Estate" || sub.Amount.String() != "1711.74 CAD" {
		t.Fatalf("subtotal = %+v, want Total Real Estate 1711.74 CAD", sub)
	}
	// A subsection of a single account (Fuel) is left alone: its subtotal would repeat the line.
	if got := lines[0].Label; got != "Expenses:Fuel" || lines[0].Subtotal {
		t.Errorf("first line = %+v, want the lone Fuel leaf with no subtotal", lines[0])
	}
}

// A subsection holding two commodities is subtotaled once per commodity, because they do not sum.
func TestWithSubtotalsSplitsByCommodity(t *testing.T) {
	rows := []reportRow{
		{Account: "Assets:Brokerage:AAPL", Amount: model.Amount{Units: 10, Commodity: "AAPL"}},
		{Account: "Assets:Brokerage:Cash", Amount: cad2(50000)},
	}
	subs := map[string]string{}
	for _, ln := range withSubtotals(rows) {
		if ln.Subtotal {
			subs[ln.Amount.Commodity] = ln.Amount.String()
		}
	}
	if subs["AAPL"] != "10 AAPL" || subs["CAD"] != "500.00 CAD" {
		t.Errorf("subtotals = %+v, want one per commodity", subs)
	}
}

// The text income statement shows a subsection's subtotal beneath its leaves.
func TestReportTextShowsSubsectionSubtotals(t *testing.T) {
	tx1, e1 := bs(1, "Expenses:Real Estate:Interest", cad2(151174), post("Assets:Bank:Chequing", cad2(-151174)))
	tx2, e2 := bs(2, "Expenses:Real Estate:Insurance", cad2(20000), post("Assets:Bank:Chequing", cad2(-20000)))
	stmt := buildReport([]model.Transaction{tx1, tx2}, []model.Entry{e1, e2}, time.Time{}, time.Time{}, "", nil)

	var buf bytes.Buffer
	renderIncomeText(&buf, stmt)
	if !strings.Contains(buf.String(), "Total Real Estate") {
		t.Errorf("text income statement missing the subsection subtotal:\n%s", buf.String())
	}
}

// The HTML balance sheet carries a subsection subtotal too, marked so a stylesheet can set it apart.
func TestReportHTMLShowsSubsectionSubtotals(t *testing.T) {
	tx1, e1 := bs(1, "Assets:Bank:Chequing", cad2(160000), post("Income:Rent", cad2(-160000)))
	tx2, e2 := bs(2, "Assets:Bank:Savings", cad2(40000), post("Income:Rent", cad2(-40000)))
	stmt := buildBalanceSheet([]model.Transaction{tx1, tx2}, []model.Entry{e1, e2}, time.Time{}, "", nil)

	var buf bytes.Buffer
	if err := renderReportHTML(&buf, reportView{Balance: &stmt}); err != nil {
		t.Fatalf("renderReportHTML: %v", err)
	}
	html := buf.String()
	for _, want := range []string{"Total Bank", "2000.00 CAD", "subtotal"} {
		if !strings.Contains(html, want) {
			t.Errorf("balance sheet HTML missing %q:\n%s", want, html)
		}
	}
}

// The HTML form carries the same statements as a self-contained page.
func TestRenderReportHTML(t *testing.T) {
	tx1, e1 := bs(1, "Assets:Bank:Chequing", cad2(160000), post("Income:Rent:123 Main", cad2(-160000)))
	tx2, e2 := bs(2, "Liabilities:Card:Visa", cad2(-20000), post("Expenses:Misc", cad2(20000)))
	txs := []model.Transaction{tx1, tx2}
	entries := []model.Entry{e1, e2}
	view := reportView{}
	stmt := buildReport(txs, entries, time.Time{}, time.Time{}, "", nil)
	sheet := buildBalanceSheet(txs, entries, time.Time{}, "", nil)
	view.Income, view.Balance = &stmt, &sheet

	var buf bytes.Buffer
	if err := renderReportHTML(&buf, view); err != nil {
		t.Fatalf("renderReportHTML: %v", err)
	}
	html := buf.String()
	for _, want := range []string{"<!doctype html>", "Income statement", "Balance sheet", "Income:Rent:123 Main", "Liabilities:Card:Visa", "Net worth"} {
		if !strings.Contains(html, want) {
			t.Errorf("report HTML missing %q", want)
		}
	}
}

// The report reads the books on the chosen basis. An invoice earned but not yet paid is invisible on
// cash (no money moved) and recognized on accrual (the day it was earned), which is the whole reason
// a landlord or consultant wants the accrual view: revenue shows when billed, not when the cheque
// clears. The report reuses the existing basis engine rather than re-deriving accrual of its own.
func TestReportOnAccrualBasisRecognizesAnUnpaidInvoice(t *testing.T) {
	log := eventlog.New(eventlog.NewMemory())
	if _, _, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(5), Party: "Acme", Amount: cad2(100000), Category: "Income:Consulting",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	from, to := on(1), on(28)

	txs, entries, err := reportEntries(log, false)
	if err != nil {
		t.Fatalf("cash: %v", err)
	}
	if cash := buildReport(txs, entries, from, to, "", nil); len(cash.Income) != 0 {
		t.Errorf("cash income = %+v, want none until the cash arrives", cash.Income)
	}

	txs, entries, err = reportEntries(log, true)
	if err != nil {
		t.Fatalf("accrual: %v", err)
	}
	if got := rowAmount(t, buildReport(txs, entries, from, to, "", nil).Income, "Income:Consulting"); got != "1000.00 CAD" {
		t.Errorf("accrual income = %q, want the earned 1000.00 CAD", got)
	}
}

// The period bounds are inclusive, and a line outside them is left out of the totals.
func TestReportFiltersByDate(t *testing.T) {
	tx1, e1 := entryOn(2, post("Income:Rent", cad2(-100000)))  // March 2
	tx2, e2 := entryOn(20, post("Income:Rent", cad2(-100000))) // March 20
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	stmt := buildReport([]model.Transaction{tx1, tx2}, []model.Entry{e1, e2}, from, to, "", nil)

	if len(stmt.Net) != 1 || stmt.Net[0].String() != "1000.00 CAD" {
		t.Errorf("net = %+v, want only the in-range line", stmt.Net)
	}
}

// seedRent books one categorized rent deposit: money into Chequing, income on the other side. It
// gives every report subcommand something to show — the income statement its earned line, the
// balance sheet the cash it landed as.
func seedRent(t *testing.T) {
	t.Helper()
	s, err := store.Open(".")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	_, err = books.Import(s.Log, "statement:test", []model.Transaction{{
		ID: "rent1", Account: "Assets:Bank:Chequing", Date: on(1),
		Amount: cad2(160000), Description: "RENT",
	}})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if err := books.Categorize(s.Log, "human", "", "rent1", "", "", "", []model.Posting{
		post("Income:Rent", cad2(-160000)),
	}); err != nil {
		t.Fatalf("Categorize: %v", err)
	}
}

// reportOut runs a report command to a temp file and returns what it wrote, so a dispatch test reads
// the rendered text without touching stdout. It pins -format text: these tests check which statement
// a subcommand shows, not what format defaults to off a terminal
// (TestReportDefaultsToJSONWhenStdoutIsNotATerminal covers that separately), and stdout is never a
// terminal under `go test`.
func reportOut(t *testing.T, args ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out.txt")
	if err := reportCmd(append(args, "-format", "text", "-out", out)); err != nil {
		t.Fatalf("reportCmd %v: %v", args, err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return string(got)
}

// report income shows only the income statement — the whole point of splitting report into one
// verb per statement rather than switching on a flag.
func TestReportIncomeShowsOnlyTheIncomeStatement(t *testing.T) {
	bookHere(t)
	seedRent(t)

	got := reportOut(t, "income")
	if !strings.Contains(got, "INCOME STATEMENT") {
		t.Errorf("report income = %q, want the income statement", got)
	}
	if strings.Contains(got, "BALANCE SHEET") || strings.Contains(got, "CAPITAL GAINS") {
		t.Errorf("report income = %q, want only the income statement", got)
	}
}

func TestReportBalanceShowsOnlyTheBalanceSheet(t *testing.T) {
	bookHere(t)
	seedRent(t)

	got := reportOut(t, "balance")
	if !strings.Contains(got, "BALANCE SHEET") {
		t.Errorf("report balance = %q, want the balance sheet", got)
	}
	if strings.Contains(got, "INCOME STATEMENT") || strings.Contains(got, "CAPITAL GAINS") {
		t.Errorf("report balance = %q, want only the balance sheet", got)
	}
}

func TestReportGainsShowsOnlyTheGainsSchedule(t *testing.T) {
	bookHere(t)
	seedRent(t)

	got := reportOut(t, "gains")
	if !strings.Contains(got, "CAPITAL GAINS") {
		t.Errorf("report gains = %q, want the gains schedule", got)
	}
	if strings.Contains(got, "INCOME STATEMENT") || strings.Contains(got, "BALANCE SHEET") {
		t.Errorf("report gains = %q, want only the gains schedule", got)
	}
}

// With no subcommand, report keeps today's combined view: the income statement and the balance
// sheet together, the "full picture" the README describes. Splitting the three statements into
// verbs should not cost this convenience.
func TestReportWithNoSubcommandShowsIncomeAndBalanceTogether(t *testing.T) {
	bookHere(t)
	seedRent(t)

	got := reportOut(t)
	if !strings.Contains(got, "INCOME STATEMENT") || !strings.Contains(got, "BALANCE SHEET") {
		t.Errorf("bare report = %q, want both statements", got)
	}
	if strings.Contains(got, "CAPITAL GAINS") {
		t.Errorf("bare report = %q, want no gains schedule unasked", got)
	}
}

func TestReportUnknownSubcommandIsRefused(t *testing.T) {
	bookHere(t)
	seedRent(t)

	if err := reportCmd([]string{"cashflow"}); err == nil {
		t.Error("an unknown report subcommand should be refused, not silently run the default")
	}
}

// Off a terminal, report defaults to json rather than the text a person at a keyboard wants -- the
// same rule books follows, so the two commands cannot drift on what "no flag given" means.
func TestReportDefaultsToJSONWhenStdoutIsNotATerminal(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1")

	out, err := withPipedStdout(t, func() error { return reportCmd(nil) })
	if err != nil {
		t.Fatalf("reportCmd: %v", err)
	}
	var parsed struct {
		Income  json.RawMessage `json:"income"`
		Balance json.RawMessage `json:"balance"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output was not JSON: %v\n%s", err, out)
	}
	if parsed.Income == nil || parsed.Balance == nil {
		t.Errorf("parsed = %+v, want both income and balance in the default view", parsed)
	}
}
