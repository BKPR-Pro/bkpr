package source

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// RBC's business-banking summary carries each account's label twice: once in the visible account
// link the person clicks, and again in a hidden "transfer to" <select> whose <option> repeats the
// same name and sits earlier in the DOM. Walking the account path by that label must click the
// visible link, not the invisible option -- clicking a hidden option never becomes actionable, so it
// times out and the run wrongly reports the account "was not found on the page". This mirrors the
// real "22 Lisgar - Loan" import that stalled: the label was present, but on a hidden option first.
// Skipped where Node or Playwright is not installed.
func TestRBCWalksPastAHiddenOptionWithTheSameLabel(t *testing.T) {
	requireBrowserTests(t)
	account, err := os.ReadFile("testdata/rbc_account.html")
	if err != nil {
		t.Fatal(err)
	}

	const dashboard = `<!doctype html><html><body>
	  <a href="/signout">Sign Out</a>
	  <a id="gotoBusinessHref" href="/business">Business Banking</a>
	</body></html>`
	// The trap: a hidden transfer-to dropdown repeats "22 Lisgar - Loan" and comes before the real
	// account link in the DOM, so a plain first-match lands on the invisible option.
	const business = `<!doctype html><html><body>
	  <a href="/signout">Sign Out</a>
	  <select style="display:none">
	    <option value=""> Select ... </option>
	    <option value="L002"> 22 Lisgar - Loan = N/A </option>
	  </select>
	  <a href="/account"><span> 22 Lisgar - Loan </span></a>
	</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/business":
			_, _ = w.Write([]byte(business))
		case "/account":
			_, _ = w.Write(account)
		default:
			_, _ = w.Write([]byte(dashboard))
		}
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution:     "rbc",
		LoginURL:        srv.URL,
		DefaultCurrency: "CAD",
		AccountPath:     []string{"#gotoBusinessHref", "22 Lisgar - Loan"},
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("walking the account path past the hidden option: %v", err)
	}

	res, err := parseBankOutput(out, "Liabilities:RBC Term Loan", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 5 {
		t.Fatalf("after reaching the account, got %d transactions, want 5: %q", len(res.Transactions), out)
	}
}
