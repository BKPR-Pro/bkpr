package source

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
