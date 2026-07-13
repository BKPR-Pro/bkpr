package source

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// options builds <option> tags 1..n (plus any extras), for the legacy date dropdowns.
func options(n int, extra ...string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, `<option value="%d">%d</option>`, i, i)
	}
	for _, e := range extra {
		fmt.Fprintf(&b, `<option value="%s">%s</option>`, e, e)
	}
	return b.String()
}

// A line of credit is read from RBC's legacy site: a Debits/Credits grid, "22 Jun 2026" dates, and a
// select-dropdown date filter with a Search button. This exercises that whole legacy branch: the from
// date is set via the dropdowns and Search reveals the grid, then the rows are read with a liability's
// sign -- a Credit (payment) positive, a Debit (advance) negative -- and the balance is the owing
// amount (the books' negation happens on the Go/CLI side).
func TestRBCReadsALegacyLineOfCredit(t *testing.T) {
	days := options(31)
	months := options(12)
	years := `<option value="2025">2025</option><option value="2026">2026</option><option value="2027">2027</option>`

	page := fmt.Sprintf(`<!doctype html><html><body>
	  <select id="DAY" name="DAY">%[1]s</select>
	  <select id="MONTH" name="MONTH">%[2]s</select>
	  <select id="YEAR" name="YEAR">%[3]s</select>
	  <select id="RDAY" name="RDAY">%[1]s</select>
	  <select id="RMONTH" name="RMONTH">%[2]s</select>
	  <select id="RYEAR" name="RYEAR">%[3]s</select>
	  <a role="button" id="id_btn_search" href="#">Search</a>
	  <div id="dlHistSort"><table><tbody id="tb"></tbody></table></div>
	  <script>
	    document.getElementById('id_btn_search').addEventListener('click', function (e) {
	      e.preventDefault()
	      // Only reveal the grid once the from-date dropdowns were set, proving the filter ran.
	      if (document.getElementById('DAY').value === '10' &&
	          document.getElementById('MONTH').value === '3' &&
	          document.getElementById('YEAR').value === '2026') {
	        document.getElementById('tb').innerHTML =
	          '<tr class="dataTableDarkRow"><td></td>' +
	          '<td class="dataTableText"> 22 Jun 2026</td>' +
	          '<td class="dataTableText"> PAYMENT</td>' +
	          '<td class="dataTableText" align="right"> </td>' +
	          '<td class="dataTableText" align="right"> 585.17</td>' +
	          '<td class="dataTableText" align="right"> 53,778.91</td></tr>' +
	          '<tr class="dataTableLightRow"><td></td>' +
	          '<td class="dataTableText"> 24 Apr 2026</td>' +
	          '<td class="dataTableText"> WWW TFR MIN0-06583</td>' +
	          '<td class="dataTableText" align="right"> 2,500.00</td>' +
	          '<td class="dataTableText" align="right"> </td>' +
	          '<td class="dataTableText" align="right"> 54,984.96</td></tr>'
	      }
	    })
	  </script>
	</body></html>`, days, months, years)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution: "rbc", LoginURL: srv.URL, DefaultCurrency: "CAD",
		HistoryFrom: "Mar 10, 2026", HistoryTo: "Jul 12, 2026",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading the legacy LOC: %v", err)
	}

	res, err := parseBankOutput(out, "Liabilities:Real Estate:RBC LOC", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 2 {
		t.Fatalf("got %d transactions, want 2: %q", len(res.Transactions), out)
	}
	// A payment is a Credit -> positive (reduces what is owed).
	if res.Transactions[0].Date.Format("2006-01-02") != "2026-06-22" ||
		res.Transactions[0].Description != "PAYMENT" ||
		res.Transactions[0].Amount.String() != "585.17 CAD" {
		t.Errorf("payment row = %+v", res.Transactions[0])
	}
	// An advance is a Debit -> negative (increases what is owed).
	if res.Transactions[1].Date.Format("2006-01-02") != "2026-04-24" ||
		res.Transactions[1].Description != "WWW TFR MIN0-06583" ||
		res.Transactions[1].Amount.String() != "-2500.00 CAD" {
		t.Errorf("advance row = %+v", res.Transactions[1])
	}
	// The balance is the owing amount, shown positive here; the CLI negates it for a liability.
	if !res.HasBalance || res.Balance.String() != "53778.91 CAD" {
		t.Errorf("balance = %s (has=%v), want 53778.91 CAD", res.Balance, res.HasBalance)
	}
}
