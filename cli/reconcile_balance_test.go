package main

import (
	"testing"

	"github.com/BKPR-Pro/bkpr/lib/model"
)

func amt(t *testing.T, s string) model.Amount {
	t.Helper()
	a, err := model.NewAmount(s, "CAD")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A bank shows every balance positive; the books hold a liability's balance owing as negative and an
// asset's as positive. reconcileBalance turns the bank figure into the books' sign so a reconcile of a
// line of credit or card matches the folded transactions.
func TestReconcileBalanceSignsByAccountType(t *testing.T) {
	if got := reconcileBalance("Assets:Consulting:Chequing", amt(t, "19102.98")); got.String() != "19102.98 CAD" {
		t.Errorf("asset balance = %s, want unchanged 19102.98 CAD", got)
	}
	if got := reconcileBalance("Liabilities:Consulting:RBC LOC", amt(t, "5000.00")); got.String() != "-5000.00 CAD" {
		t.Errorf("liability balance = %s, want negated -5000.00 CAD", got)
	}
	// The bare top-level account is treated by its type too.
	if got := reconcileBalance("Liabilities", amt(t, "1.00")); got.String() != "-1.00 CAD" {
		t.Errorf("bare liability = %s, want -1.00 CAD", got)
	}
}
