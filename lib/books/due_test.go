package books_test

import (
	"testing"

	"bkpr.pro/bkpr/lib/books"
)

// Owing a liability account is enough to show up in the due report, even before anyone has ever
// set a due date or minimum on it -- the blank columns are the nudge to fill them in.
func TestDueAccountsListsAnOwingLiabilityWithNoMetaSet(t *testing.T) {
	log := newLog()
	importOne(t, log, lineIn("a", "Liabilities:PC Mastercard", 2, -8420, "PAYMENT"))

	rows, err := books.DueAccounts(log)
	if err != nil {
		t.Fatalf("DueAccounts: %v", err)
	}
	if len(rows) != 1 || rows[0].Account != "Liabilities:PC Mastercard" {
		t.Fatalf("rows = %+v, want one row for the card", rows)
	}
	if rows[0].Due != "" || rows[0].Minimum != "" {
		t.Errorf("due = %q, minimum = %q, want both blank", rows[0].Due, rows[0].Minimum)
	}
	if got := rows[0].Balance["CAD"]; got.String() != "-84.20 CAD" {
		t.Errorf("balance = %s", got)
	}
}

// An Assets: account never belongs in a liability due report, however much it owes or holds.
func TestDueAccountsExcludesNonLiabilityAccounts(t *testing.T) {
	log := newLog()
	importOne(t, log, lineIn("a", "Assets:Bank:Chequing", 2, -8420, "GROCERIES"))

	rows, err := books.DueAccounts(log)
	if err != nil {
		t.Fatalf("DueAccounts: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// A liability account that nets to zero -- paid in full -- has nothing owing, so it drops out of
// the report the same way a settled bill drops out of aging.
func TestDueAccountsExcludesAZeroBalanceLiability(t *testing.T) {
	log := newLog()
	importOne(t, log, lineIn("a", "Liabilities:PC Mastercard", 2, -8420, "PURCHASE"))
	importOne(t, log, lineIn("b", "Liabilities:PC Mastercard", 3, 8420, "PAYMENT IN FULL"))

	rows, err := books.DueAccounts(log)
	if err != nil {
		t.Fatalf("DueAccounts: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none for a fully paid card", rows)
	}
}

// The due date and minimum payment set on an account via `accounts set -meta` fold straight
// through to the report.
func TestDueAccountsSurfacesDueAndMinimumMeta(t *testing.T) {
	log := newLog()
	importOne(t, log, lineIn("a", "Liabilities:PC Mastercard", 2, -8420, "PURCHASE"))
	if err := books.SetAccountMeta(log, "human", "Liabilities:PC Mastercard", map[string]string{
		"due": "2026-08-05", "minimum": "25.00",
	}); err != nil {
		t.Fatalf("SetAccountMeta: %v", err)
	}

	rows, err := books.DueAccounts(log)
	if err != nil {
		t.Fatalf("DueAccounts: %v", err)
	}
	if len(rows) != 1 || rows[0].Due != "2026-08-05" || rows[0].Minimum != "25.00" {
		t.Fatalf("rows = %+v", rows)
	}
}

// A physical card split by purpose across several :child accounts nets to one row under the bare
// parent, not one row per bucket -- a purpose bucket can look positive on its own even though the
// card overall is owed money, and the report exists to say what is actually owed.
func TestDueAccountsRollsUpAnAccountFamilyIntoOneNetRow(t *testing.T) {
	log := newLog()
	importOne(t, log, lineIn("a", "Liabilities:RBC Mastercard", 2, 37661, "PAYMENT"))
	importOne(t, log, lineIn("b", "Liabilities:RBC Mastercard:Consulting", 2, -52432, "CHARGE"))
	importOne(t, log, lineIn("c", "Liabilities:RBC Mastercard:Real Estate:9 Schoodic Street", 2, -95273, "CHARGE"))
	books.SetAccountMeta(log, "human", "Liabilities:RBC Mastercard", map[string]string{
		"due": "2026-08-05", "minimum": "25.00",
	})

	rows, err := books.DueAccounts(log)
	if err != nil {
		t.Fatalf("DueAccounts: %v", err)
	}
	if len(rows) != 1 || rows[0].Account != "Liabilities:RBC Mastercard" {
		t.Fatalf("rows = %+v, want one row for the family", rows)
	}
	if got := rows[0].Balance["CAD"]; got.String() != "-1100.44 CAD" {
		t.Errorf("balance = %s, want the family net", got)
	}
	if rows[0].Due != "2026-08-05" || rows[0].Minimum != "25.00" {
		t.Errorf("due = %q, minimum = %q, want the parent's meta", rows[0].Due, rows[0].Minimum)
	}
}

// A purpose bucket that nets to zero on its own still folds into the family total; only the family
// as a whole dropping to zero removes the row.
func TestDueAccountsKeepsAFamilyRowWhenOnlyAChildBucketIsZero(t *testing.T) {
	log := newLog()
	importOne(t, log, lineIn("a", "Liabilities:RBC Mastercard", 2, -100, "CHARGE"))
	importOne(t, log, lineIn("b", "Liabilities:RBC Mastercard:Consulting", 2, -8420, "CHARGE"))
	importOne(t, log, lineIn("c", "Liabilities:RBC Mastercard:Consulting", 3, 8420, "PAYMENT IN FULL"))

	rows, err := books.DueAccounts(log)
	if err != nil {
		t.Fatalf("DueAccounts: %v", err)
	}
	if len(rows) != 1 || rows[0].Account != "Liabilities:RBC Mastercard" {
		t.Fatalf("rows = %+v, want one row for the family", rows)
	}
	if got := rows[0].Balance["CAD"]; got.String() != "-1.00 CAD" {
		t.Errorf("balance = %s, want just the parent's own charge", got)
	}
}

// Owing accounts come back sorted by due date, soonest first, so the accounts that need attention
// lead the report; accounts with no due date set sort after every dated one.
func TestDueAccountsSortsBySoonestDueDate(t *testing.T) {
	log := newLog()
	importOne(t, log, lineIn("a", "Liabilities:PC Mastercard", 2, -8420, "PURCHASE"))
	importOne(t, log, lineIn("b", "Liabilities:Simplii LOC", 2, -50000, "DRAW"))
	importOne(t, log, lineIn("c", "Liabilities:RBC Visa", 2, -3000, "PURCHASE"))
	books.SetAccountMeta(log, "human", "Liabilities:PC Mastercard", map[string]string{"due": "2026-08-05"})
	books.SetAccountMeta(log, "human", "Liabilities:Simplii LOC", map[string]string{"due": "2026-07-30"})
	// Liabilities:RBC Visa is left with no due date.

	rows, err := books.DueAccounts(log)
	if err != nil {
		t.Fatalf("DueAccounts: %v", err)
	}
	want := []string{"Liabilities:Simplii LOC", "Liabilities:PC Mastercard", "Liabilities:RBC Visa"}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v", rows)
	}
	for i, a := range want {
		if rows[i].Account != a {
			t.Errorf("rows[%d] = %s, want %s", i, rows[i].Account, a)
		}
	}
}
