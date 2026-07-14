package source

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A card lists Pending and Posted transactions in separate tables, and the Posted table's Filter sits
// behind its own Search button (a chequing has neither). applyDateRange must open that Posted search
// first, then Filter, then the dates, then Apply. Here the Filter is hidden until the Posted search is
// clicked, and the row only appears after the whole chain runs -- so the test fails if the Posted
// search step is skipped.
func TestRBCCardWidensViaPostedSearch(t *testing.T) {
	requireBrowserTests(t)
	const page = `<!doctype html><html><body>
	  <table aria-label="Table 2: Posted transactions details">
	    <tbody><tr><td><button id="posted" type="button">Search</button></td></tr></tbody>
	  </table>
	  <button id="filter" type="button" style="display:none">Filter</button>
	  <div id="panel" style="display:none">
	    <input id="rbc-dp-0" type="text"><input id="rbc-dp-1" type="text">
	    <button id="apply" type="button" aria-label="Apply">Apply</button>
	  </div>
	  <table class="rbc-transaction-list-table"><tbody id="tb"></tbody></table>
	  <script>
	    document.getElementById('posted').addEventListener('click', function () {
	      document.getElementById('filter').style.display = 'inline' })
	    document.getElementById('filter').addEventListener('click', function () {
	      document.getElementById('panel').style.display = 'block' })
	    document.getElementById('apply').addEventListener('click', function () {
	      if (document.getElementById('rbc-dp-0').value.trim() !== '' &&
	          document.getElementById('rbc-dp-1').value.trim() !== '') {
	        document.getElementById('tb').innerHTML =
	          '<tr data-role="transaction-list-table-transaction" class="rbc-transaction-list-transaction-new">' +
	          '<td class="date-column-padding" headers="date" id="2026-02-05"> Feb 5, 2026 </td>' +
	          '<td class="rbc-transaction-list-desc"><div> OLD CHARGE </div></td>' +
	          '<td class="rbc-transaction-list-withdraw"><span>$40.00</span></td>' +
	          '<td class="rbc-transaction-list-balance"> $40.00 </td></tr>'
	      }
	    })
	  </script>
	</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution: "rbc", LoginURL: srv.URL, DefaultCurrency: "CAD",
		Account:     "Liabilities:Consulting:RBC Visa",
		HistoryFrom: "Feb 1, 2026", HistoryTo: "Jul 13, 2026",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("widening the card via the posted search: %v", err)
	}

	res, err := parseBankOutput(out, "Liabilities:Consulting:RBC Visa", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 1 {
		t.Fatalf("got %d transactions, want 1 (the older charge, revealed only after the full flow): %q", len(res.Transactions), out)
	}
	// The charge is a liability increase -> negative in the books.
	if res.Transactions[0].Date.Format("2006-01-02") != "2026-02-05" || res.Transactions[0].Amount.String() != "-40.00 CAD" {
		t.Errorf("row = %+v", res.Transactions[0])
	}
}
