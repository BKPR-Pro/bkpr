package main

import (
	"strings"
	"testing"

	"github.com/dallasread/bkpr/lib/model"
)

func mkAmt(units int64, comm string) model.Amount {
	return model.Amount{Units: units, Scale: 2, Commodity: comm}
}

// The amount sort key is a signed decimal, so money out sorts below money in and a bigger balance
// sorts above a smaller one.
func TestAmountMagnitudeIsSignedDecimal(t *testing.T) {
	if got := amountMagnitude(mkAmt(1050, "CAD")); got != 10.50 {
		t.Errorf("magnitude of $10.50 = %v, want 10.50", got)
	}
	if !(amountMagnitude(mkAmt(-500, "CAD")) < amountMagnitude(mkAmt(300, "CAD"))) {
		t.Error("money out (-5.00) should sort below money in (3.00)")
	}
}

// accounts -sort amount orders by balance, ascending by default and descending on request; equal
// balances fall back to the name so the order is stable.
func TestOrderAccountsByAmount(t *testing.T) {
	bal := map[string]map[string]model.Amount{
		"A": {"CAD": mkAmt(10000, "CAD")}, // 100
		"B": {"CAD": mkAmt(-5000, "CAD")}, // -50
		"C": {"CAD": mkAmt(20000, "CAD")}, // 200
	}
	names := []string{"A", "B", "C"}
	if got := strings.Join(orderAccountsByAmount(names, bal, false), ","); got != "B,A,C" {
		t.Errorf("ascending = %s, want B,A,C", got)
	}
	if got := strings.Join(orderAccountsByAmount(names, bal, true), ","); got != "C,A,B" {
		t.Errorf("descending = %s, want C,A,B", got)
	}
}

// A multi-commodity account sums its per-commodity figures for the sort key, so it can still be placed
// against single-commodity accounts.
func TestOrderAccountsByAmountSumsCommodities(t *testing.T) {
	bal := map[string]map[string]model.Amount{
		"Mix": {"CAD": mkAmt(10000, "CAD"), "USD": mkAmt(10000, "USD")}, // 200
		"One": {"CAD": mkAmt(15000, "CAD")},                             // 150
	}
	if got := strings.Join(orderAccountsByAmount([]string{"Mix", "One"}, bal, false), ","); got != "One,Mix" {
		t.Errorf("ascending = %s, want One,Mix", got)
	}
}

// books -sort amount reorders the lines by amount while keeping each transaction paired with its entry.
func TestOrderLinesByAmountKeepsPairing(t *testing.T) {
	txs := []model.Transaction{
		{ID: "x", Amount: mkAmt(1000, "CAD")}, // 10
		{ID: "y", Amount: mkAmt(-500, "CAD")}, // -5
		{ID: "z", Amount: mkAmt(300, "CAD")},  // 3
	}
	entries := []model.Entry{{Payee: "x"}, {Payee: "y"}, {Payee: "z"}}

	st, se := orderLinesByAmount(txs, entries, false)
	want := []string{"y", "z", "x"}
	for k, w := range want {
		if st[k].ID != w {
			t.Errorf("ascending position %d = %s, want %s", k, st[k].ID, w)
		}
		if se[k].Payee != st[k].ID {
			t.Errorf("position %d: entry %q not paired with tx %q", k, se[k].Payee, st[k].ID)
		}
	}

	stD, _ := orderLinesByAmount(txs, entries, true)
	if stD[0].ID != "x" || stD[len(stD)-1].ID != "y" {
		t.Errorf("descending should run x..y, got %s..%s", stD[0].ID, stD[len(stD)-1].ID)
	}
}
