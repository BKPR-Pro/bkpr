package books_test

import (
	"testing"

	"github.com/dallasread/bkpr/lib/books"
	"github.com/dallasread/bkpr/lib/model"
)

// postingsOf finds the accrual line a party's name renders and returns its postings, so a tax test
// can ask what the fold actually booked.
func postingsOf(t *testing.T, entries []model.Entry, payee string) []model.Posting {
	t.Helper()
	for _, e := range entries {
		if e.Payee == payee {
			return e.Postings
		}
	}
	t.Fatalf("no accrual line for %q", payee)
	return nil
}

// A commercial rent invoice carries sales tax: the amount billed is the gross owed, and the fold
// splits it into the revenue earned and the tax collected on the tenant's behalf. The receivable
// stays the full gross, because that is what the tenant owes.
func TestARaisedInvoiceSplitsItsTax(t *testing.T) {
	log := newLog()
	_, _, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(1), Party: "Commercial Tenant", Amount: cad(115000),
		Category:   "Income:Real Estate:Rent:Unit A",
		TaxRate:    "15%",
		TaxAccount: "Liabilities:Real Estate:HST:Rent:Unit A",
	})
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}

	txs, entries := booksOn(t, log, books.AccrualBasis)
	ps := postingsOf(t, entries, "Commercial Tenant")
	if len(ps) != 2 {
		t.Fatalf("got %d postings, want the income leg and the tax leg: %+v", len(ps), ps)
	}
	if ps[0].Account != "Income:Real Estate:Rent:Unit A" || ps[0].Amount.String() != "-1000.00 CAD" {
		t.Errorf("income posting = %+v, want a -1000.00 CAD credit to the rent account", ps[0])
	}
	if ps[1].Account != "Liabilities:Real Estate:HST:Rent:Unit A" || ps[1].Amount.String() != "-150.00 CAD" {
		t.Errorf("tax posting = %+v, want a -150.00 CAD credit to the HST account", ps[1])
	}
	if got := balances(txs, entries)["Assets:Receivable"]; got != 115000 {
		t.Errorf("receivable = %d, want the full 115000 gross the tenant owes", got)
	}
}

// A bill's recoverable tax is the mirror: the expense takes the pre-tax amount and the tax lands on
// its own account as an input credit, with the payable holding the full gross.
func TestAReceivedBillSplitsItsTax(t *testing.T) {
	log := newLog()
	_, _, err := books.ReceiveBill(log, "human", "", books.Bill{
		Date: on(1), Party: "ACME Hardware", Amount: cad(115000),
		Category:   "Expenses:Repairs",
		TaxRate:    "15%",
		TaxAccount: "Assets:HST Recoverable",
	})
	if err != nil {
		t.Fatalf("ReceiveBill: %v", err)
	}

	txs, entries := booksOn(t, log, books.AccrualBasis)
	ps := postingsOf(t, entries, "ACME Hardware")
	if len(ps) != 2 {
		t.Fatalf("got %d postings, want the expense leg and the tax leg: %+v", len(ps), ps)
	}
	if ps[0].Account != "Expenses:Repairs" || ps[0].Amount.String() != "1000.00 CAD" {
		t.Errorf("expense posting = %+v, want a 1000.00 CAD debit to Expenses:Repairs", ps[0])
	}
	if ps[1].Account != "Assets:HST Recoverable" || ps[1].Amount.String() != "150.00 CAD" {
		t.Errorf("tax posting = %+v, want a 150.00 CAD debit to the recoverable account", ps[1])
	}
	if got := balances(txs, entries)["Liabilities:Payable"]; got != -115000 {
		t.Errorf("payable = %d, want the full -115000 gross owed", got)
	}
}

// The tax fields ride through the log: they are part of the recorded fact, not a rendering choice,
// so a folded invoice or bill comes back holding them.
func TestTaxFieldsRoundTripThroughTheLog(t *testing.T) {
	log := newLog()
	if _, _, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(1), Party: "Commercial Tenant", Amount: cad(115000),
		Category: "Income:Rent", TaxRate: "15%", TaxAccount: "Liabilities:HST",
	}); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if _, _, err := books.ReceiveBill(log, "human", "", books.Bill{
		Date: on(1), Party: "ACME Hardware", Amount: cad(115000),
		Category: "Expenses:Repairs", TaxRate: "13.5%", TaxAccount: "Assets:HST Recoverable",
	}); err != nil {
		t.Fatalf("ReceiveBill: %v", err)
	}

	invs, err := books.Invoices(log)
	if err != nil {
		t.Fatalf("Invoices: %v", err)
	}
	if len(invs) != 1 || invs[0].TaxRate != "15%" || invs[0].TaxAccount != "Liabilities:HST" {
		t.Fatalf("folded invoices = %+v, want the tax fields kept", invs)
	}
	bills, err := books.Bills(log)
	if err != nil {
		t.Fatalf("Bills: %v", err)
	}
	if len(bills) != 1 || bills[0].TaxRate != "13.5%" || bills[0].TaxAccount != "Assets:HST Recoverable" {
		t.Fatalf("folded bills = %+v, want the tax fields kept", bills)
	}
}

// A tax rate and a tax account are one fact in two halves, exactly as they are on a rule: either
// alone is refused, and so is a rate that does not read as a percentage.
func TestTaxRateAndAccountAreRequiredTogether(t *testing.T) {
	log := newLog()
	base := books.Invoice{Date: on(1), Party: "J. Smith", Amount: cad(115000), Category: "Income:Consulting"}

	half := base
	half.TaxAccount = "Liabilities:HST"
	if _, _, err := books.Raise(log, "human", "", half); err == nil {
		t.Error("raised an invoice with a tax account and no rate")
	}
	half = base
	half.TaxRate = "15%"
	if _, _, err := books.Raise(log, "human", "", half); err == nil {
		t.Error("raised an invoice with a tax rate and no account")
	}
	bad := base
	bad.TaxRate, bad.TaxAccount = "0.15", "Liabilities:HST"
	if _, _, err := books.Raise(log, "human", "", bad); err == nil {
		t.Error("raised an invoice with a rate that is not a percentage")
	}

	b := books.Bill{Date: on(1), Party: "ACME", Amount: cad(115000), Category: "Expenses:Repairs"}
	b.TaxAccount = "Assets:HST Recoverable"
	if _, _, err := books.ReceiveBill(log, "human", "", b); err == nil {
		t.Error("received a bill with a tax account and no rate")
	}
	b.TaxAccount, b.TaxRate = "", "15%"
	if _, _, err := books.ReceiveBill(log, "human", "", b); err == nil {
		t.Error("received a bill with a tax rate and no account")
	}
}

// Fingerprints are the settlement key real books already hold, so an untaxed accrual must hash to
// exactly what it hashes to today: moving these ids would orphan every recorded settlement.
func TestAnUntaxedAccrualKeepsItsFingerprint(t *testing.T) {
	log := newLog()
	inv := raise(t, log, "J. Smith", 1, 160000, "Income:Consulting")
	if inv.ID != "9617607456a06619" {
		t.Errorf("invoice fingerprint = %q, want the id today's books already hold", inv.ID)
	}
	b, _, err := books.ReceiveBill(log, "human", "", books.Bill{
		Date: on(1), Party: "ACME Hardware", Amount: cad(8420), Category: "Expenses:Repairs",
	})
	if err != nil {
		t.Fatalf("ReceiveBill: %v", err)
	}
	if b.ID != "9a0c79bf8aa41be8" {
		t.Errorf("bill fingerprint = %q, want the id today's books already hold", b.ID)
	}
}

// Two invoices alike but for their tax are different facts, so the tax belongs in the hash: without
// it, recording the taxed one after the untaxed one would collapse into a no-op.
func TestTaxMakesADistinctFingerprint(t *testing.T) {
	log := newLog()
	plain := raise(t, log, "J. Smith", 1, 115000, "Income:Consulting")
	taxed, added, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(1), Party: "J. Smith", Amount: cad(115000), Category: "Income:Consulting",
		TaxRate: "15%", TaxAccount: "Liabilities:HST",
	})
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if !added {
		t.Error("the taxed invoice collapsed onto the untaxed one")
	}
	if taxed.ID == plain.ID {
		t.Errorf("both invoices fingerprint to %q; the tax must distinguish them", taxed.ID)
	}
}

// Settlement is unchanged by tax: the cash clears the whole gross parked in the receivable, and the
// income and the tax it recognized stay booked once.
func TestSettlingATaxedInvoiceClearsTheGross(t *testing.T) {
	log := newLog()
	inv, _, err := books.Raise(log, "human", "", books.Invoice{
		Date: on(1), Party: "J. Smith", Amount: cad(115000), Category: "Income:Consulting",
		TaxRate: "15%", TaxAccount: "Liabilities:HST",
	})
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	loaded(t, log, rule("j smith", "Income:Consulting"))
	importOne(t, log, line("pay", 20, 115000, "E-TRANSFER FROM J SMITH"))
	if err := books.SettleInvoice(log, "human", inv.ID, "pay"); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	bal := balances(booksOn(t, log, books.AccrualBasis))
	if bal["Assets:Receivable"] != 0 {
		t.Errorf("receivable = %d, want 0 once the gross was paid", bal["Assets:Receivable"])
	}
	if bal["Income:Consulting"] != -100000 {
		t.Errorf("income = %d, want -100000 booked exactly once", bal["Income:Consulting"])
	}
	if bal["Liabilities:HST"] != -15000 {
		t.Errorf("hst = %d, want -15000 booked exactly once", bal["Liabilities:HST"])
	}
}
