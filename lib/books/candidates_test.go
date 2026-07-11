package books_test

import (
	"testing"
	"time"

	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/model"
)

// An open invoice offers the deposit that plausibly settles it: same amount, within the window, not
// already spoken for. This is the fingerprint-hunting the settle step used to require, surfaced.
func TestAnOpenInvoiceOffersItsCandidateDeposit(t *testing.T) {
	log := newLog()
	inv := raise(t, log, "J. Smith", 1, 160000, "Income:Consulting")
	importOne(t, log, line("pay", 20, 160000, "E-TRANSFER FROM J SMITH")) // a matching deposit
	importOne(t, log, line("other", 21, -6240, "SHELL GAS"))              // wrong amount and sign

	cands, err := books.SettlementCandidates(log)
	if err != nil {
		t.Fatalf("SettlementCandidates: %v", err)
	}
	got := cands[inv.ID]
	if len(got) != 1 || got[0] != "pay" {
		t.Fatalf("candidates = %v, want [pay]", got)
	}
}

// A bill offers the payment that clears it: the sign is opposite an invoice's, so a payment out of
// the bank matches a payable and a deposit does not.
func TestAnOpenBillOffersItsCandidatePayment(t *testing.T) {
	log := newLog()
	b := receive(t, log, "Power Co", 1, 50000, "Expenses:Utilities:Power")
	importOne(t, log, line("pay", 15, -50000, "POWER CO PREAUTH")) // money out, matches the payable
	importOne(t, log, line("dep", 16, 50000, "A DEPOSIT"))         // same magnitude, wrong sign

	cands, err := books.SettlementCandidates(log)
	if err != nil {
		t.Fatalf("SettlementCandidates: %v", err)
	}
	got := cands[b.ID]
	if len(got) != 1 || got[0] != "pay" {
		t.Fatalf("candidates = %v, want [pay]", got)
	}
}

// A line already settling one accrual is spoken for, so it is never offered for another of the same
// amount. Otherwise two invoices for the same sum would both claim the one deposit.
func TestASettlingLineIsNotOfferedTwice(t *testing.T) {
	log := newLog()
	a := raise(t, log, "J. Smith", 1, 160000, "Income:Consulting")
	b := raise(t, log, "K. Jones", 2, 160000, "Income:Consulting")
	importOne(t, log, line("pay", 20, 160000, "A DEPOSIT"))
	if err := books.SettleInvoice(log, "human", a.ID, "pay"); err != nil {
		t.Fatalf("SettleInvoice: %v", err)
	}

	cands, err := books.SettlementCandidates(log)
	if err != nil {
		t.Fatalf("SettlementCandidates: %v", err)
	}
	if got := cands[a.ID]; len(got) != 0 {
		t.Errorf("settled invoice still offers candidates: %v", got)
	}
	if got := cands[b.ID]; len(got) != 0 {
		t.Errorf("the used deposit was offered to the other invoice: %v", got)
	}
}

// A deposit dated far from the invoice is not offered: beyond the window it is almost certainly a
// different payment.
func TestADistantDepositIsNotACandidate(t *testing.T) {
	log := newLog()
	inv := raise(t, log, "J. Smith", 1, 160000, "Income:Consulting")
	importOne(t, log, model.Transaction{ // ~a year later, well beyond the window
		ID: "far", Account: "Assets:Bank:Chequing", Date: time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC),
		Amount: cad(160000), Description: "E-TRANSFER FROM J SMITH",
	})

	cands, err := books.SettlementCandidates(log)
	if err != nil {
		t.Fatalf("SettlementCandidates: %v", err)
	}
	if got := cands[inv.ID]; len(got) != 0 {
		t.Errorf("a deposit a year later was offered: %v", got)
	}
}
