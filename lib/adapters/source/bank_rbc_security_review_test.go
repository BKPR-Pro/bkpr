package source

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Once "That was me" is confirmed, RBC serves a prompt to review the Personal Verification Questions
// before continuing. A person gets past it from the left navigation -- Account Summary -- but that
// navigation is script-injected and absent from the served markup, so the sign-in walked into it and
// then blamed the account-navigation step for a missing element.
//
// The way through that IS in the markup is BalancesForm (REQUEST=AcctBalanceInquiry), the same request
// the Account Summary link submits: plain navigation to a balances view. The page's one visible
// control, "Update my security settings", changes security settings and must be left alone -- only
// the account holder decides their verification questions.
func TestRBCPassesSecurityReviewViaAccountSummary(t *testing.T) {
	requireBrowserTests(t)
	loginForm := readFixture(t, "rbc_login_form.html")
	review := readFixture(t, "rbc_security_review.html")
	account := readFixture(t, "rbc_account.html")

	var mu sync.Mutex
	reachedSummary, touchedSettings := false, false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			mu.Lock()
			switch {
			case r.FormValue("REQUEST") == "AcctBalanceInquiry":
				reachedSummary = true
			case r.FormValue("REQUEST") == "DispPVQsA" || strings.Contains(r.URL.Path, "2-step-verification"):
				touchedSettings = true
			}
			mu.Unlock()
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := r.Cookie("bk_session"); err != nil {
			_, _ = w.Write(loginForm)
			return
		}
		mu.Lock()
		through := reachedSummary
		mu.Unlock()
		if through {
			_, _ = w.Write(account)
			return
		}
		if !strings.Contains(r.URL.Path, "ISAMSecureRequest") {
			http.Redirect(w, r, "/sgw5/SECOLBH/3m00/ISAMSecureRequest/v1/eBGRenderPage", http.StatusSeeOther)
			return
		}
		_, _ = w.Write(review)
	}))
	defer srv.Close()

	creds := map[string]string{"username": "test-card", "password": "test-pass"}
	out, err := execBankScript(Bank{Institution: "rbc", LoginURL: srv.URL, DefaultCurrency: "CAD"}, creds)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("sign-in through the security-review prompt: %v", err)
	}

	mu.Lock()
	sawSummary, sawSettings := reachedSummary, touchedSettings
	mu.Unlock()

	if !sawSummary {
		t.Error("never navigated to the account summary, so the review prompt was never cleared")
	}
	if sawSettings {
		t.Error("submitted the security-settings form -- verification questions are the account holder's to change, never the connector's")
	}

	res, err := parseBankOutput(out, "test-connector", "Assets:Bank:RBC", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 5 {
		t.Fatalf("after clearing the review prompt, got %d transactions, want 5: %q", len(res.Transactions), out)
	}
}
