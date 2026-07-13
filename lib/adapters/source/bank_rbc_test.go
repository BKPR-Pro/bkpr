package source

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// serveFixture serves one testdata HTML file and returns its URL.
func serveFixture(t *testing.T, name string) string {
	t.Helper()
	html, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(html)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// skipIfNoBrowser turns "Node/Playwright not installed" into a skip, since then no browser can drive
// the page; any other error is a real failure.
func skipIfNoBrowser(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	msg := err.Error()
	// The stealth path (Simplii) needs patchright and a real Chrome; skip where either is absent, the
	// same way we skip when node or Playwright's browser is missing.
	for _, want := range []string{"node not found", "playwright", "patchright", "chrome", "Chromium", "channel"} {
		if strings.Contains(msg, want) {
			t.Skipf("browser tooling unavailable: %v", err)
		}
	}
}

// The RBC profile is exercised for real: the embedded scripts/rbc.js runs through the harness and a
// headless browser against a fixture that mirrors RBC's actual account-page DOM, and its output is
// normalized exactly as a live import would be. This pins the three things rbc.js must get right --
// which rows are transactions (not the duplicated responsive table, not the day-header rows), how a
// withdrawal/deposit becomes a signed amount, and how "Jul 11, 2026" becomes 2026-07-11 -- so a
// selector or format drift is caught here instead of by an empty statement. Skipped where Node or
// Playwright is not installed, since then no browser can drive the page.
func TestRBCScriptReadsAccountPage(t *testing.T) {
	url := serveFixture(t, "rbc_account.html")

	out, err := execBankScript(Bank{Institution: "rbc", LoginURL: url, DefaultCurrency: "CAD"}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("running rbc.js against the fixture: %v", err)
	}

	res, err := parseBankOutput(out, "Assets:Bank:RBC", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}

	// The desktop grid holds five transactions; the responsive table's duplicate row must not be
	// counted, and the day-header rows are not transactions.
	want := []struct {
		date, desc, amount string
	}{
		{"2026-07-11", "Online Banking payment", "-150.00 CAD"},
		{"2026-07-10", "Contactless Interac purchase - 0001 B001 TEST CAFE", "-12.34 CAD"},
		{"2026-07-10", "e-Transfer - Autodeposit TEST PERSON ref123", "500.00 CAD"},
		{"2026-07-09", "Misc Payment TEST CORP", "1234.56 CAD"},
		{"2026-06-30", "Monthly fee", "-6.00 CAD"},
	}
	if len(res.Transactions) != len(want) {
		t.Fatalf("got %d transactions, want %d: %q", len(res.Transactions), len(want), out)
	}
	for i, w := range want {
		tx := res.Transactions[i]
		if got := tx.Date.Format("2006-01-02"); got != w.date {
			t.Errorf("row %d date = %s, want %s", i, got, w.date)
		}
		if tx.Description != w.desc {
			t.Errorf("row %d description = %q, want %q", i, tx.Description, w.desc)
		}
		if tx.Amount.String() != w.amount {
			t.Errorf("row %d amount = %s, want %s", i, tx.Amount.String(), w.amount)
		}
		if tx.ID == "" {
			t.Errorf("row %d was not fingerprinted", i)
		}
	}

	// The newest row's running balance is the account's current posted balance, an asset shown
	// positive, carried for reconciliation.
	if !res.HasBalance || res.Balance.String() != "1000.00 CAD" {
		t.Errorf("balance = %s (has=%v), want 1000.00 CAD", res.Balance, res.HasBalance)
	}
}

// When the harness lands on RBC's sign-in page with no session and no person present, isLoginWall
// recognizes it and the run fails as a session that needs refreshing (EX_TEMPFAIL -> ErrSessionExpired)
// rather than reading the login page as an empty statement.
func TestRBCScriptDetectsLoginWall(t *testing.T) {
	url := serveFixture(t, "rbc_login.html")

	_, err := execBankScript(Bank{Institution: "rbc", LoginURL: url, DefaultCurrency: "CAD", Interactive: false}, nil)
	skipIfNoBrowser(t, err)
	if !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("on the sign-in page, non-interactive, err = %v, want ErrSessionExpired", err)
	}
}
