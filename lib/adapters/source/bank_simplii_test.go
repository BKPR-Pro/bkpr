package source

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// padOptions builds <option value="01">1</option> ... value="0n", the zero-padded values Simplii's
// month and day selects use (and widenHistory selects by).
func padOptions(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, `<option value="%02d">%d</option>`, i, i)
	}
	return b.String()
}

// signedInPage wraps Simplii account markup with the signed-in landmark (the profile initials link)
// so the profile's isLoginWall reports false and no sign-in is attempted.
func signedInSimplii(body string) string {
	return `<!doctype html><html><body class="ember-application">
	  <a data-test-id="sidebar-profile-link-header" href="#">RR</a>
	  ` + body + `
	</body></html>`
}

// The sign-in fields render inside the auth widget's iframe, so signIn must find them across frames,
// fill card + password, and click the real "Sign in" button (data-test-id="primary-button"). Here the
// top page has no signed-in landmark until the iframe posts back after a filled submit, at which point
// the account picker + transactions appear -- so the test passes only if the cross-frame fill and
// submit actually happened, then the reader runs.
func TestSimpliiSignsInAcrossFramesThenReads(t *testing.T) {
	requireBrowserTests(t)
	const cardHTML = `<!doctype html><html><body>
	  <input data-test-id="card-number-input" type="text">
	  <input data-test-id="password-input" type="password">
	  <button data-test-id="primary-button" type="button">Sign in</button>
	  <script>
	    document.querySelector('[data-test-id="primary-button"]').addEventListener('click', function () {
	      if (document.querySelector('[data-test-id="card-number-input"]').value &&
	          document.querySelector('[data-test-id="password-input"]').value) {
	        window.parent.postMessage('signed-in', '*')
	      }
	    })
	  </script>
	</body></html>`

	// The signed-in view the top page reveals once the iframe reports a successful sign-in.
	const account = `<select aria-label="Select an account. This page will refresh upon selection."><option>Personal Line of Credit</option></select>
	  <div class="tombstone"><div class="row"><div class="box-small balance"><span>Balance:</span><em>−$49,671.86</em></div></div></div>
	  <section class="transaction-list row"><table><tbody>
	    <tr><td class="date">Jul 10, 2026</td>
	      <td class="transactions"><span class="transactionDescription">TRANSFER OUT</span></td>
	      <td class="debit"><span>$1,000.00</span></td>
	      <td class="credit"><span class="hidden-text">Not applicable</span></td>
	      <td class="balance"><span class="negative">−$49,671.86</span></td></tr>
	  </tbody></table></section>`

	topHTML := `<!doctype html><html><body class="ember-application">
	  <iframe title="empty" src="/card" style="width:400px;height:200px;border:0"></iframe>
	  <div id="content"></div>
	  <script>
	    window.addEventListener('message', function (e) {
	      if (e.data === 'signed-in') document.getElementById('content').innerHTML = ` + "`" + account + "`" + `
	    })
	  </script>
	</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/card" {
			_, _ = w.Write([]byte(cardHTML))
		} else {
			_, _ = w.Write([]byte(topHTML))
		}
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution: "simplii", LoginURL: srv.URL, DefaultCurrency: "CAD",
		Account: "Liabilities:Real Estate:Simplii LOC",
	}, map[string]string{"username": "4500000000000000", "password": "hunter2"})
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("signing in across frames: %v", err)
	}

	res, err := parseBankOutput(out, "Liabilities:Real Estate:Simplii LOC", "CAD")
	if err != nil {
		t.Fatalf("parsing simplii.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 1 || res.Transactions[0].Amount.String() != "-1000.00 CAD" {
		t.Fatalf("expected the one row read after sign-in, got %d: %q", len(res.Transactions), out)
	}
	if !res.HasBalance || res.Balance.String() != "49671.86 CAD" {
		t.Errorf("balance = %s (has=%v), want 49671.86 CAD", res.Balance, res.HasBalance)
	}
}

// Simplii's line of credit and chequing render the same transaction table, read one way: Funds in
// (credit) positive, Funds out (debit) negative -- already the books' sign for the liability line of
// credit (whose balance Simplii shows negative) and the asset chequing alike. Runs through the stealth
// path (patchright + real Chrome), so it is skipped where that tooling is absent.
func TestSimpliiReadsALineOfCredit(t *testing.T) {
	requireBrowserTests(t)
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

// A history window drives Simplii's custom date search: widenHistory fills the From (and To) date --
// each a month/day/year <select> -- and clicks Get Details. The fixture reveals an older row only once
// the From date holds the requested start, so the test passes only if -from actually reached the
// date fields.
func TestSimpliiReadsAWiderHistoryWindow(t *testing.T) {
	requireBrowserTests(t)
	months := padOptions(12)
	days := padOptions(31)
	years := `<option value="2025">2025</option><option value="2026">2026</option>`
	dateRow := func(container string) string {
		return `<div class="` + container + `">
		  <div class="ui-month"><select>` + months + `</select></div>
		  <div class="ui-date"><select>` + days + `</select></div>
		  <div class="ui-year"><select>` + years + `</select></div>
		</div>`
	}

	body := `
	  <div class="filter-by-range">` + dateRow("from") + dateRow("to") + `</div>
	  <button type="button" id="getDetails">Get Details</button>
	  <section class="transaction-list row"><table><tbody id="tb">
	    <tr>
	      <td class="date">Jul 10, 2026</td>
	      <td class="transactions"><span class="transactionDescription">RECENT</span></td>
	      <td class="debit"><span class="hidden-text">Not applicable</span></td>
	      <td class="credit"><span>$50.00</span></td>
	      <td class="balance"><span class="negative">−$49,671.86</span></td>
	    </tr>
	  </tbody></table></section>
	  <script>
	    document.getElementById('getDetails').addEventListener('click', function () {
	      var f = document.querySelector('.filter-by-range .from')
	      if (f.querySelector('.ui-month select').value === '02' &&
	          f.querySelector('.ui-date select').value === '01' &&
	          f.querySelector('.ui-year select').value === '2026') {
	        document.getElementById('tb').insertAdjacentHTML('beforeend',
	          '<tr><td class="date">Feb 5, 2026</td>' +
	          '<td class="transactions"><span class="transactionDescription">OLDER DRAW</span></td>' +
	          '<td class="debit"><span>$200.00</span></td>' +
	          '<td class="credit"><span class="hidden-text">Not applicable</span></td>' +
	          '<td class="balance"><span class="negative">−$50,000.00</span></td></tr>')
	      }
	    })
	  </script>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(signedInSimplii(body)))
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution: "simplii", LoginURL: srv.URL, DefaultCurrency: "CAD",
		Account:     "Liabilities:Real Estate:Simplii LOC",
		HistoryFrom: "Feb 1, 2026", HistoryTo: "Jul 13, 2026",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading with a Simplii history window: %v", err)
	}

	res, err := parseBankOutput(out, "Liabilities:Real Estate:Simplii LOC", "CAD")
	if err != nil {
		t.Fatalf("parsing simplii.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 2 {
		t.Fatalf("got %d transactions, want 2 (recent + older via the date range): %q", len(res.Transactions), out)
	}
	if res.Transactions[1].Date.Format("2006-01-02") != "2026-02-05" || res.Transactions[1].Description != "OLDER DRAW" {
		t.Errorf("older row not loaded from the date range: %+v", res.Transactions[1])
	}
}

// The chequing uses tr.transaction-row (the LOC uses bare tr); keying off td.date reads both. As an
// asset its balance is positive and taken as-is.
func TestSimpliiReadsChequing(t *testing.T) {
	requireBrowserTests(t)
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
