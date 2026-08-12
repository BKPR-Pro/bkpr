package books_test

import (
	"testing"

	"github.com/BKPR-Pro/bkpr/lib/books"
	"github.com/BKPR-Pro/bkpr/lib/eventlog"
)

func accountMeta(t *testing.T, log *eventlog.Log, account string) map[string]string {
	t.Helper()
	all, err := books.AccountMeta(log)
	if err != nil {
		t.Fatalf("AccountMeta: %v", err)
	}
	return all[account]
}

// A source that declares accounts records their metadata in one call, so an import can carry the
// letterhead a hand-kept file names alongside the transactions it carries.
func TestImportAccountMetaRecordsEachAccount(t *testing.T) {
	log := newLog()
	n, err := books.ImportAccountMeta(log, "human", map[string]map[string]string{
		"Assets:Bank:Chequing": {"address": "888888 Example Inc.\n10 Maple Street"},
		"Liabilities:Bank:HST": {"address": "HST"},
	})
	if err != nil {
		t.Fatalf("ImportAccountMeta: %v", err)
	}
	if n != 2 {
		t.Fatalf("recorded %d, want 2", n)
	}
	if got := accountMeta(t, log, "Assets:Bank:Chequing")["address"]; got != "888888 Example Inc.\n10 Maple Street" {
		t.Errorf("address = %q", got)
	}
}

// Re-importing the same file records nothing new: an account whose stored metadata already matches
// is skipped, so the log does not grow an event on every re-run of a migration.
func TestImportAccountMetaSkipsUnchanged(t *testing.T) {
	log := newLog()
	meta := map[string]map[string]string{"Assets:Bank:Chequing": {"address": "10 Maple Street"}}
	if _, err := books.ImportAccountMeta(log, "human", meta); err != nil {
		t.Fatalf("ImportAccountMeta: %v", err)
	}
	before, _ := log.All()
	n, err := books.ImportAccountMeta(log, "human", meta)
	if err != nil {
		t.Fatalf("ImportAccountMeta: %v", err)
	}
	after, _ := log.All()
	if n != 0 || len(after) != len(before) {
		t.Fatalf("re-import recorded %d and grew the log from %d to %d", n, len(before), len(after))
	}
}

// Metadata set on an account folds back out under that account, keyed by the path.
func TestAccountMetaFoldsBackByAccount(t *testing.T) {
	log := newLog()
	if err := books.SetAccountMeta(log, "human", "Assets:Bank:Chequing", map[string]string{
		"name": "Northwind Studio", "address": "123 Main St\nOttawa ON",
	}); err != nil {
		t.Fatalf("SetAccountMeta: %v", err)
	}
	m := accountMeta(t, log, "Assets:Bank:Chequing")
	if m["name"] != "Northwind Studio" || m["address"] != "123 Main St\nOttawa ON" {
		t.Errorf("meta = %+v", m)
	}
}

// Keys merge across calls, so an address recorded now and a name recorded later both survive.
func TestAccountMetaMergesPerKey(t *testing.T) {
	log := newLog()
	books.SetAccountMeta(log, "human", "Income:Consulting:Acme", map[string]string{"address": "1 Client Rd"})
	if err := books.SetAccountMeta(log, "human", "Income:Consulting:Acme", map[string]string{"name": "Acme Corp"}); err != nil {
		t.Fatalf("SetAccountMeta: %v", err)
	}
	m := accountMeta(t, log, "Income:Consulting:Acme")
	if m["address"] != "1 Client Rd" {
		t.Errorf("the earlier key was dropped: %+v", m)
	}
	if m["name"] != "Acme Corp" {
		t.Errorf("the later key is missing: %+v", m)
	}
}

// Restating a key is another fact, and the latest wins.
func TestAccountMetaLatestKeyWins(t *testing.T) {
	log := newLog()
	books.SetAccountMeta(log, "human", "Assets:Bank:Chequing", map[string]string{"address": "old"})
	books.SetAccountMeta(log, "human", "Assets:Bank:Chequing", map[string]string{"address": "new"})
	if got := accountMeta(t, log, "Assets:Bank:Chequing")["address"]; got != "new" {
		t.Errorf("address = %q, want the latest", got)
	}
}

// An empty account, or a set with nothing in it, is refused rather than recorded as a no-op fact.
func TestAccountMetaRefusesEmptyInput(t *testing.T) {
	log := newLog()
	if err := books.SetAccountMeta(log, "human", "", map[string]string{"name": "X"}); err == nil {
		t.Error("an empty account should be refused")
	}
	if err := books.SetAccountMeta(log, "human", "Assets:Bank", map[string]string{}); err == nil {
		t.Error("setting no metadata should be refused")
	}
}
