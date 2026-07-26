package source

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// An account reached by an account path lands on a page that renders late: RBC is an Angular app, and
// the route's own DOM (a card's Posted-transactions search, its Filter, its rows) arrives a beat after
// the click, while the summary is still on screen. navigatePath must wait for the destination before
// the reader runs, because everything downstream tests the page ONCE and walks on when it is not there
// yet: applyDateRange reads a missing Filter as "this account has no filter UI" and silently skips the
// requested window, and the snapshot captures the page that is leaving rather than the one arriving.
//
// Here the card's controls appear 1200ms after navigation, and the Apply handler only renders the row
// when both dates were filled -- so a run that walked past the unrendered page reads nothing.
func TestRBCWaitsForALateRenderingAccountPage(t *testing.T) {
	requireBrowserTests(t)

	const summary = `<!doctype html><html><body>
	  <a href="/signout">Sign Out</a>
	  <a href="/card">Cash Back Mastercard</a>
	</body></html>`

	// The card route: an empty shell that fills in after a beat, the way the SPA settles.
	const card = `<!doctype html><html><body>
	  <a href="/signout">Sign Out</a>
	  <div id="late"></div>
	  <table class="rbc-transaction-list-table"><tbody id="tb"></tbody></table>
	  <script>
	    setTimeout(function () {
	      document.getElementById('late').innerHTML =
	        '<table aria-label="Table 2: Posted transactions details">' +
	        '<tbody><tr><td><button id="posted" type="button">Search</button></td></tr></tbody></table>' +
	        '<button id="filter" type="button" style="display:none">Filter</button>' +
	        '<div id="panel" style="display:none">' +
	        '<input id="rbc-dp-0" type="text"><input id="rbc-dp-1" type="text">' +
	        '<button id="apply" type="button" aria-label="Apply">Apply</button></div>'
	      document.getElementById('posted').addEventListener('click', function () {
	        document.getElementById('filter').style.display = 'inline' })
	      document.getElementById('filter').addEventListener('click', function () {
	        document.getElementById('panel').style.display = 'block' })
	      document.getElementById('apply').addEventListener('click', function () {
	        if (document.getElementById('rbc-dp-0').value.trim() !== '' &&
	            document.getElementById('rbc-dp-1').value.trim() !== '') {
	          document.getElementById('tb').innerHTML =
	            '<tr data-role="transaction-list-table-transaction" class="rbc-transaction-list-transaction-new">' +
	            '<td class="date-column-padding" headers="cc-date-large" id="2026-07-20"> Jul 20, 2026 </td>' +
	            '<td class="rbc-transaction-list-desc"><div> LATE CHARGE </div></td>' +
	            '<td class="rbc-transaction-list-withdraw"><span>$25.00</span></td></tr>'
	        }
	      })
	    }, 1200)
	  </script>
	</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/card" {
			_, _ = w.Write([]byte(card))
			return
		}
		_, _ = w.Write([]byte(summary))
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution: "rbc", LoginURL: srv.URL, DefaultCurrency: "CAD",
		Account:     "Liabilities:RBC Mastercard",
		AccountPath: []string{"Cash Back Mastercard"},
		HistoryFrom: "Jul 11, 2026", HistoryTo: "Jul 26, 2026",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading a late-rendering account page: %v", err)
	}

	res, err := parseBankOutput(out, "test-connector", "Liabilities:RBC Mastercard", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 1 {
		t.Fatalf("got %d transactions, want 1 -- the date filter was skipped on the unrendered page: %q",
			len(res.Transactions), out)
	}
}
