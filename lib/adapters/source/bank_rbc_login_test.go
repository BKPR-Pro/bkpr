package source

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// An unattended sign-in is exercised for real: the fixture server shows RBC's two-step login form
// until a session cookie is set, then the account page, mimicking how a real session cookie makes the
// account URL resolve to the account instead of the login wall. rbc.js, handed a username and
// password over stdin, types the form headless, the harness saves the session and re-navigates, and
// the transactions are read -- proving credentials flow from Go to the browser and drive a login
// without a person. Skipped where Node or Playwright is not installed.
func TestRBCSignsInWithCredentials(t *testing.T) {
	requireBrowserTests(t)
	loginForm, err := os.ReadFile("testdata/rbc_login_form.html")
	if err != nil {
		t.Fatal(err)
	}
	account, err := os.ReadFile("testdata/rbc_account.html")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := r.Cookie("bk_session"); err == nil {
			_, _ = w.Write(account) // signed in
		} else {
			_, _ = w.Write(loginForm) // login wall
		}
	}))
	defer srv.Close()

	creds := map[string]string{"username": "test-card", "password": "test-pass"}
	out, err := execBankScript(Bank{Institution: "rbc", LoginURL: srv.URL, DefaultCurrency: "CAD"}, creds)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("unattended rbc.js sign-in: %v", err)
	}

	res, err := parseBankOutput(out, "Assets:Bank:RBC", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 5 {
		t.Fatalf("after signing in, got %d transactions, want 5: %q", len(res.Transactions), out)
	}
	if !res.HasBalance || res.Balance.String() != "1000.00 CAD" {
		t.Errorf("balance = %s (has=%v), want 1000.00 CAD", res.Balance, res.HasBalance)
	}
}

// Without credentials and with no person present, an expired session still fails fast rather than
// hanging on a login form it cannot fill.
func TestRBCWithoutCredentialsIsSessionExpired(t *testing.T) {
	requireBrowserTests(t)
	url := serveFixture(t, "rbc_login_form.html")

	_, err := execBankScript(Bank{Institution: "rbc", LoginURL: url, DefaultCurrency: "CAD", Interactive: false}, nil)
	skipIfNoBrowser(t, err)
	if err == nil || !strings.Contains(err.Error(), "session") {
		t.Fatalf("no creds, non-interactive, err = %v, want a session-expired failure", err)
	}
}
