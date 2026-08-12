package main

import (
	"testing"
	"time"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/store"
)

// reconOf reopens the book and returns the reconciliation for one account.
func reconOf(t *testing.T, account string) books.Reconciliation {
	t.Helper()
	s, err := store.Open(".")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	recs, err := books.Reconcile(s.Log)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, r := range recs {
		if r.Account == account {
			return r
		}
	}
	t.Fatalf("no reconciliation for %s", account)
	return books.Reconciliation{}
}

// FR-4: a CSV/ledger account has no connector to record a bank balance, so `balance set` records one
// by hand. The first assertion anchors the account, so it reconciles by definition at that point --
// the account can now be checked to the penny without waiting on a connector.
func TestBalanceSetAnchorsAFileImportedAccount(t *testing.T) {
	bookHere(t)
	seedTx(t, "tx1") // one -10.00 line into Assets:Bank:Chequing

	if err := balanceCmd([]string{"set", "Assets:Bank:Chequing", "100.00 CAD", "-as-of", "2026-03-31"}); err != nil {
		t.Fatalf("balance set: %v", err)
	}

	r := reconOf(t, "Assets:Bank:Chequing")
	if !r.Reconciled {
		t.Errorf("the first hand-set balance should anchor and reconcile, got delta %s", r.Delta)
	}
	if r.Bank.String() != "100.00 CAD" {
		t.Errorf("bank = %s, want 100.00 CAD", r.Bank)
	}
	if !r.AsOf.Equal(time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("as-of = %s, want 2026-03-31", r.AsOf.Format("2006-01-02"))
	}
}

// A bank shows a liability's balance owing as a positive figure; the books hold it negative. A
// hand-set balance takes the same sign treatment a connector's does, so a line of credit anchored by
// hand matches its folded (negative) transactions.
func TestBalanceSetSignsALiabilityOwing(t *testing.T) {
	bookHere(t)

	if err := balanceCmd([]string{"set", "Liabilities:Consulting:Acme LOC", "5000.00 CAD"}); err != nil {
		t.Fatalf("balance set: %v", err)
	}

	r := reconOf(t, "Liabilities:Consulting:Acme LOC")
	if r.Bank.String() != "-5000.00 CAD" {
		t.Errorf("bank = %s, want -5000.00 CAD (owing stored negative)", r.Bank)
	}
}

// `set` needs an account and an amount; a missing amount is a clear error, not a silent no-op.
func TestBalanceSetNeedsAnAmount(t *testing.T) {
	bookHere(t)
	if err := balanceCmd([]string{"set", "Assets:Bank:Chequing"}); err == nil {
		t.Fatal("balance set with no amount should error")
	}
}
