package source

import (
	"fmt"
	"strings"
	"testing"
)

// stubBank swaps the browser runner for a fake for the duration of a test.
func stubBank(t *testing.T, out []byte, err error) {
	t.Helper()
	prev := runBank
	runBank = func(Bank) ([]byte, error) { return out, err }
	t.Cleanup(func() { runBank = prev })
}

// The importable banks are recognized; anything else (including the old "scrape") is not.
func TestSupportsBank(t *testing.T) {
	for _, kind := range []string{"rbc", "simplii", "pcfinancial"} {
		if !SupportsBank(kind) {
			t.Errorf("SupportsBank(%q) = false, want true", kind)
		}
	}
	for _, kind := range []string{"scrape", "rentapp", "nope", ""} {
		if SupportsBank(kind) {
			t.Errorf("SupportsBank(%q) = true, want false", kind)
		}
	}
}

// ReadBank normalizes and fingerprints the script's JSON the same way ReadCSV does, so a bank import
// and a CSV of one account are interchangeable.
func TestReadBankNormalizesRows(t *testing.T) {
	stubBank(t, []byte(`[
		{"date":"2026-03-01","description":"SHELL GAS","amount":"-62.40"},
		{"date":"2026-03-02","description":"USD CHARGE","amount":"-10.00","currency":"USD"}
	]`), nil)

	res, err := ReadBank(Bank{Institution: "rbc", Account: "Liabilities:Card:RBC", DefaultCurrency: "CAD"})
	if err != nil {
		t.Fatalf("ReadBank: %v", err)
	}
	txs := res.Transactions
	if len(txs) != 2 {
		t.Fatalf("got %d transactions, want 2", len(txs))
	}
	if txs[0].Account != "Liabilities:Card:RBC" || txs[0].Amount.String() != "-62.40 CAD" || txs[0].ID == "" {
		t.Errorf("row 0 = %+v", txs[0])
	}
	if txs[1].Amount.String() != "-10.00 USD" { // a row's own currency wins over the default
		t.Errorf("row 1 currency = %q, want the row's own USD", txs[1].Amount.String())
	}
	if res.HasBalance {
		t.Errorf("a bare array has no balance, got %s", res.Balance)
	}
}

// A script may wrap its rows with the account's current balance; ReadBank carries it in the account's
// currency for reconciliation.
func TestReadBankCarriesTheScrapedBalance(t *testing.T) {
	stubBank(t, []byte(`{"rows":[{"date":"2026-03-01","description":"SHELL","amount":"-62.40"}],"balance":"1842.00"}`), nil)

	res, err := ReadBank(Bank{Institution: "rbc", Account: "Assets:Bank:RBC", DefaultCurrency: "CAD"})
	if err != nil {
		t.Fatalf("ReadBank: %v", err)
	}
	if len(res.Transactions) != 1 {
		t.Fatalf("got %d transactions, want 1", len(res.Transactions))
	}
	if !res.HasBalance || res.Balance.String() != "1842.00 CAD" {
		t.Errorf("balance = %s (has=%v), want 1842.00 CAD", res.Balance, res.HasBalance)
	}
}

// A run that fails (a login that did not take, a stubbed script) stops the import with the script's
// message rather than recording an empty statement.
func TestReadBankPropagatesRunnerError(t *testing.T) {
	stubBank(t, nil, fmt.Errorf("import rbc: the RBC Playwright script is a stub"))
	if _, err := ReadBank(Bank{Institution: "rbc", Account: "X", DefaultCurrency: "CAD"}); err == nil {
		t.Fatal("a failed run should stop the import")
	}
}

// A bank with no script is refused, naming the ones that exist.
func TestReadBankUnknownInstitution(t *testing.T) {
	_, err := ReadBank(Bank{Institution: "td", Account: "X", DefaultCurrency: "CAD"})
	if err == nil || !strings.Contains(err.Error(), "rbc") {
		t.Fatalf("err = %v, want a refusal that lists the known banks", err)
	}
}

// The embedded scripts run for real through Node and the harness. Without Playwright installed (or
// with an unreachable site) an import fails loudly rather than silently recording nothing. Skipped
// where Node itself is not installed.
func TestScriptsFailLoudlyWithoutABrowser(t *testing.T) {
	// An unreachable URL so the run fails quickly even where Playwright happens to be installed, and
	// non-interactive so it never tries to open a browser.
	_, err := execBankScript(Bank{Institution: "rbc", LoginURL: "http://127.0.0.1:0/"})
	if err != nil && strings.Contains(err.Error(), "node not found") {
		t.Skip("Node not installed")
	}
	if err == nil {
		t.Fatal("without a reachable browser/site the RBC import should fail, not record nothing")
	}
}
