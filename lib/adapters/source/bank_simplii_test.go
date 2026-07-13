package source

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// signedInPage wraps Simplii account markup with the signed-in landmark (the profile initials link)
// so the profile's isLoginWall reports false and no sign-in is attempted.
func signedInSimplii(body string) string {
	return `<!doctype html><html><body class="ember-application">
	  <a data-test-id="sidebar-profile-link-header" href="#">RR</a>
	  ` + body + `
	</body></html>`
}

// Simplii's line of credit and chequing render the same transaction table, read one way: Funds in
// (credit) positive, Funds out (debit) negative -- already the books' sign for the liability line of
// credit (whose balance Simplii shows negative) and the asset chequing alike. Runs through the stealth
// path (patchright + real Chrome), so it is skipped where that tooling is absent.
func TestSimpliiReadsALineOfCredit(t *testing.T) {
	body := `
	  <div class="tombstone"><div class="row">
	    <div class="box-small balance"><span>Balance:</span><em>−$49,671.86</em></div>
	    <div class="box-small"><span>Available Funds:</span><em>$328.14</em></div>
	  </div></div>
	  <section class="transaction-list row"><table><tbody>
	    <tr>
	      <td class="date">Jul 10, 2026</td>
	      <td class="transactions"><span class="transactionLocation"></span><span class="transactionDescription"> TRANSFER OUT  </span></td>
	      <td class="debit"><span>$1,000.00</span></td>
	      <td class="credit"><span class="hidden-text">Not applicable</span></td>
	      <td class="balance"><span class="negative">−$49,671.86</span></td>
	    </tr>
	    <tr>
	      <td class="date">Jul 7, 2026</td>
	      <td class="transactions"><span class="transactionDescription"> INTERAC E-TRANSFER RECEIVE 742104 NB INC. </span></td>
	      <td class="debit"><span class="hidden-text">Not applicable</span></td>
	      <td class="credit"><span>$600.00</span></td>
	      <td class="balance"><span class="negative">−$48,671.86</span></td>
	    </tr>
	    <tr>
	      <td class="date">Jun 28, 2026</td>
	      <td class="transactions"><span class="transactionDescription"> INTEREST CHARGE  </span></td>
	      <td class="debit"><span>$405.71</span></td>
	      <td class="credit"><span class="hidden-text">Not applicable</span></td>
	      <td class="balance"><span class="negative">−$49,271.86</span></td>
	    </tr>
	  </tbody></table></section>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(signedInSimplii(body)))
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution: "simplii", LoginURL: srv.URL, DefaultCurrency: "CAD",
		Account: "Liabilities:Real Estate:Simplii LOC",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading the Simplii LOC: %v", err)
	}

	res, err := parseBankOutput(out, "Liabilities:Real Estate:Simplii LOC", "CAD")
	if err != nil {
		t.Fatalf("parsing simplii.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 3 {
		t.Fatalf("got %d transactions, want 3: %q", len(res.Transactions), out)
	}
	// A draw (Funds out) increases what is owed -> negative.
	if res.Transactions[0].Date.Format("2006-01-02") != "2026-07-10" ||
		res.Transactions[0].Description != "TRANSFER OUT" ||
		res.Transactions[0].Amount.String() != "-1000.00 CAD" {
		t.Errorf("draw row = %+v", res.Transactions[0])
	}
	// A payment (Funds in) reduces what is owed -> positive.
	if res.Transactions[1].Amount.String() != "600.00 CAD" ||
		res.Transactions[1].Description != "INTERAC E-TRANSFER RECEIVE 742104 NB INC." {
		t.Errorf("payment row = %+v", res.Transactions[1])
	}
	if res.Transactions[2].Description != "INTEREST CHARGE" || res.Transactions[2].Amount.String() != "-405.71 CAD" {
		t.Errorf("interest row = %+v", res.Transactions[2])
	}
	// Balance is the owing magnitude; the CLI negates it for the liability.
	if !res.HasBalance || res.Balance.String() != "49671.86 CAD" {
		t.Errorf("balance = %s (has=%v), want 49671.86 CAD", res.Balance, res.HasBalance)
	}
}

// The chequing uses tr.transaction-row (the LOC uses bare tr); keying off td.date reads both. As an
// asset its balance is positive and taken as-is.
func TestSimpliiReadsChequing(t *testing.T) {
	body := `
	  <div class="tombstone"><div class="row tombstone-regular">
	    <div class="box-small"><span>Balance:</span><em>$1,000.00</em></div>
	  </div></div>
	  <section class="transaction-list row"><table><tbody>
	    <tr class="transaction-row">
	      <td class="date">Jul 10, 2026</td>
	      <td class="transactions"><span class="transactionDescription"> TRANSFER IN  </span></td>
	      <td class="debit"><span class="hidden-text">Not applicable</span></td>
	      <td class="credit"><span>$1,000.00</span></td>
	      <td class="balance"><span>$1,000.00</span></td>
	    </tr>
	    <tr class="transaction-row">
	      <td class="date">Jun 15, 2026</td>
	      <td class="transactions"><span class="transactionDescription"> TRANSFER OUT  </span></td>
	      <td class="debit"><span>$1,000.00</span></td>
	      <td class="credit"><span class="hidden-text">Not applicable</span></td>
	      <td class="balance"><span>$0.00</span></td>
	    </tr>
	  </tbody></table></section>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(signedInSimplii(body)))
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution: "simplii", LoginURL: srv.URL, DefaultCurrency: "CAD",
		Account: "Assets:Personal:Simplii Chequing",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading the Simplii chequing: %v", err)
	}

	res, err := parseBankOutput(out, "Assets:Personal:Simplii Chequing", "CAD")
	if err != nil {
		t.Fatalf("parsing simplii.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 2 {
		t.Fatalf("got %d transactions, want 2: %q", len(res.Transactions), out)
	}
	if res.Transactions[0].Amount.String() != "1000.00 CAD" || res.Transactions[1].Amount.String() != "-1000.00 CAD" {
		t.Errorf("rows = %+v, %+v", res.Transactions[0], res.Transactions[1])
	}
	if !res.HasBalance || res.Balance.String() != "1000.00 CAD" {
		t.Errorf("balance = %s (has=%v), want 1000.00 CAD", res.Balance, res.HasBalance)
	}
}
