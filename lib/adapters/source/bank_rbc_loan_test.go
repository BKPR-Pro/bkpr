package source

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A term loan is a balance-only legacy account: it shows a "Current balance" summary and no
// transaction grid at all (its activity is the monthly payment, which appears in the chequing). The
// import records the balance -- owing, so negative once the CLI applies the liability sign -- and
// reads zero transactions rather than failing.
func TestRBCReadsABalanceOnlyLoan(t *testing.T) {
	requireBrowserTests(t)
	const page = `<!doctype html><html><body class="template-legacy">
	  <h1 id="pagetitle">22 Lisgar - Loan</h1>
	  <table>
	    <tr>
	      <th class="fieldLabel">Current  balance:</th>
	      <td class="bodyText">$86,523.31&nbsp;<b></b></td>
	    </tr>
	    <tr>
	      <th class="fieldLabel">Interest rate:</th>
	      <td class="bodyText">7.00%</td>
	    </tr>
	  </table>
	</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{Institution: "rbc", LoginURL: srv.URL, DefaultCurrency: "CAD"}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading the loan: %v", err)
	}

	res, err := parseBankOutput(out, "test-connector", "Liabilities:Real Estate:22 Lisgar - Loan", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 0 {
		t.Errorf("a term loan has no transactions, got %d: %q", len(res.Transactions), out)
	}
	// The owing balance, shown positive here; the CLI negates it for the liability.
	if !res.HasBalance || res.Balance.String() != "86523.31 CAD" {
		t.Errorf("balance = %s (has=%v), want 86523.31 CAD", res.Balance, res.HasBalance)
	}
}

// A history window is passed to every connector in a run, including the loan -- whose balance-only
// page has no date-range form at all. Applying the window there must be recognized as impossible
// immediately, not by waiting out an action timeout per missing form control (six selects and a
// Search button at 30s each is minutes of dead time for an account with nothing to read).
func TestRBCBalanceOnlyLoanIgnoresAHistoryWindowQuickly(t *testing.T) {
	requireBrowserTests(t)
	const page = `<!doctype html><html><body class="template-legacy">
	  <h1 id="pagetitle">22 Lisgar - Loan</h1>
	  <table>
	    <tr>
	      <th class="fieldLabel">Current  balance:</th>
	      <td class="bodyText">$86,523.31&nbsp;<b></b></td>
	    </tr>
	  </table>
	</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	start := time.Now()
	out, err := execBankScript(Bank{
		Institution: "rbc", LoginURL: srv.URL, DefaultCurrency: "CAD",
		HistoryFrom: "Mar 10, 2026", HistoryTo: "Jul 12, 2026",
	}, nil)
	elapsed := time.Since(start)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading the loan with a history window: %v", err)
	}
	// Generous against a slow browser launch, far under one 30s action timeout.
	if elapsed > 25*time.Second {
		t.Errorf("balance-only loan with a history window took %s; the missing date form should be detected instantly", elapsed)
	}

	res, err := parseBankOutput(out, "test-connector", "Liabilities:Real Estate:22 Lisgar - Loan", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 0 {
		t.Errorf("a term loan has no transactions, got %d: %q", len(res.Transactions), out)
	}
	if !res.HasBalance || res.Balance.String() != "86523.31 CAD" {
		t.Errorf("balance = %s (has=%v), want 86523.31 CAD", res.Balance, res.HasBalance)
	}
}
