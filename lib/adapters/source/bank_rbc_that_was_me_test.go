package source

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// After the password, RBC can interpose a "Sign-In Protection Alert" -- it flags an earlier
// unsuccessful attempt, disables Online Banking, and asks "Did you try signing in on this date?"
// with "That was me" / "That was not me". Confirming is the unlock path, so a sign-in that walks
// past it reaches no account at all; the observed symptom is the account-navigation step blaming a
// missing nav element on a page that is really still the alert.
//
// The fixture renders its controls a beat after the page loads, the way RBC's SPA does. That is the
// whole point of the test: a guard that only reads the CURRENT visibility of "That was me" sees an
// empty page and moves on, while one that waits for it confirms and gets through.
func TestRBCConfirmsSignInProtectionAlert(t *testing.T) {
	requireBrowserTests(t)
	loginForm := readFixture(t, "rbc_login_form.html")
	alert := readFixture(t, "rbc_signin_alert.html")
	account := readFixture(t, "rbc_account.html")

	var mu sync.Mutex
	confirmed, deniedIt := false, false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			switch r.URL.Path {
			case "/thatwasme":
				confirmed = true
			case "/thatwasnotme":
				deniedIt = true
			}
			mu.Unlock()
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := r.Cookie("bk_session"); err != nil {
			_, _ = w.Write(loginForm) // login wall
			return
		}

		mu.Lock()
		done := confirmed
		mu.Unlock()
		if done {
			_, _ = w.Write(account)
			return
		}
		// RBC serves the alert from an ISAMSecureRequest URL, so reproduce that path too: it is the
		// branch the old code took, and it looked only for a "Continue" this page does not have.
		if !strings.Contains(r.URL.Path, "ISAMSecureRequest") {
			http.Redirect(w, r, "/sgw5/SECOLBH/3m00/ISAMSecureRequest/v1/eBGRenderPage", http.StatusSeeOther)
			return
		}
		_, _ = w.Write(alert)
	}))
	defer srv.Close()

	creds := map[string]string{"username": "test-card", "password": "test-pass"}
	out, err := execBankScript(Bank{Institution: "rbc", LoginURL: srv.URL, DefaultCurrency: "CAD"}, creds)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("sign-in through the protection alert: %v", err)
	}

	mu.Lock()
	sawConfirm, sawDeny := confirmed, deniedIt
	mu.Unlock()

	if !sawConfirm {
		t.Error("never clicked \"That was me\", so Online Banking stayed disabled")
	}
	// The two controls sit side by side and their labels overlap as substrings; clicking the wrong
	// one reports the user's own sign-in as fraud.
	if sawDeny {
		t.Error("clicked \"That was not me\" -- that reports the sign-in as fraudulent")
	}

	res, err := parseBankOutput(out, "test-connector", "Assets:Bank:RBC", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 5 {
		t.Fatalf("after confirming the alert, got %d transactions, want 5: %q", len(res.Transactions), out)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
