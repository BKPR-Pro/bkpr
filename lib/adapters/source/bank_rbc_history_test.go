package source

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// With a history window, rbc.js drives RBC's date-range filter: open the Filter panel (which reveals
// the date fields), type the from and to dates, Apply, then page "Show More" until it is gone. The
// fixture reveals the older transaction only after that whole sequence, and Apply proceeds only once
// both date fields hold a value -- so the test passes only if the panel was opened, both fields were
// filled, Apply ran, and Show More paged the rest in.
func TestRBCReadsAWiderHistoryWindow(t *testing.T) {
	const page = `<!doctype html><html><body>
	  <button id="filter" type="button">Filter</button>
	  <div id="panel" style="display:none">
	    <input id="rbc-dp-0" type="text" placeholder="Select...">
	    <input id="rbc-dp-1" type="text" placeholder="Select...">
	    <button id="apply" type="button" aria-label="Apply">Apply</button>
	  </div>
	  <button id="more" type="button" style="display:none">Show More</button>
	  <table class="rbc-transaction-list-table"><tbody id="tb">
	    <tr data-role="transaction-list-table-transaction" class="rbc-transaction-list-transaction-new">
	      <td headers="date" id="2026-07-11"> Jul 11, 2026 </td>
	      <td class="rbc-transaction-list-desc"><div> Recent </div></td>
	      <td class="rbc-transaction-list-withdraw"><span>-$1.00</span></td>
	      <td class="rbc-transaction-list-balance"> $100.00 </td>
	    </tr>
	  </tbody></table>
	  <script>
	    var applied = false
	    document.getElementById('filter').addEventListener('click', function () {
	      document.getElementById('panel').style.display = 'block' })
	    document.getElementById('apply').addEventListener('click', function () {
	      var f = document.getElementById('rbc-dp-0').value.trim()
	      var t = document.getElementById('rbc-dp-1').value.trim()
	      if (f !== '' && t !== '') { applied = true; document.getElementById('more').style.display = 'inline' }
	    })
	    document.getElementById('more').addEventListener('click', function () {
	      if (applied) {
	        document.getElementById('tb').insertAdjacentHTML('beforeend',
	          '<tr data-role="transaction-list-table-transaction" class="rbc-transaction-list-transaction-new">' +
	          '<td headers="date" id="2026-04-02"> Apr 2, 2026 </td>' +
	          '<td class="rbc-transaction-list-desc"><div> Older </div></td>' +
	          '<td class="rbc-transaction-list-deposit"><span>$2.00</span></td>' +
	          '<td class="rbc-transaction-list-balance"> </td></tr>')
	      }
	      this.style.display = 'none'
	    })
	  </script>
	</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution: "rbc", LoginURL: srv.URL, DefaultCurrency: "CAD", HistoryDays: 100,
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading with a history window: %v", err)
	}

	res, err := parseBankOutput(out, "Assets:Bank:RBC", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 2 {
		t.Fatalf("got %d transactions, want 2 (recent + older via the range filter): %q", len(res.Transactions), out)
	}
	if got := res.Transactions[1].Date.Format("2006-01-02"); got != "2026-04-02" {
		t.Errorf("older transaction not loaded; second date = %s", got)
	}
}

// An explicit from/to range drives the same filter as the relative window, so a backfill of a known
// period reads the same way.
func TestRBCReadsAnExplicitDateRange(t *testing.T) {
	// A card's filter labels its date fields "Date Range From"/"Date Range To" rather than giving them
	// the chequing's #rbc-dp ids, so this exercises the label path.
	const page = `<!doctype html><html><body>
	  <button id="filter" type="button">Filter</button>
	  <div id="panel" style="display:none">
	    <input aria-label="Date Range From" type="text">
	    <input aria-label="Date Range To" type="text">
	    <button id="apply" type="button" aria-label="Apply">Apply</button>
	  </div>
	  <table class="rbc-transaction-list-table"><tbody id="tb"></tbody></table>
	  <script>
	    document.getElementById('filter').addEventListener('click', function () {
	      document.getElementById('panel').style.display = 'block' })
	    document.getElementById('apply').addEventListener('click', function () {
	      // Reveal a row only if both dates were entered, and stamp it with the typed from-date so the
	      // test can confirm the explicit range reached the field.
	      var f = document.querySelector('[aria-label="Date Range From"]').value.trim()
	      var t = document.querySelector('[aria-label="Date Range To"]').value.trim()
	      if (f !== '' && t !== '') {
	        document.getElementById('tb').innerHTML =
	          '<tr data-role="transaction-list-table-transaction" class="rbc-transaction-list-transaction-new">' +
	          '<td headers="date" id="2026-02-01"> Feb 1, 2026 </td>' +
	          '<td class="rbc-transaction-list-desc"><div>' + f + '</div></td>' +
	          '<td class="rbc-transaction-list-withdraw"><span>-$3.00</span></td>' +
	          '<td class="rbc-transaction-list-balance"> $9.00 </td></tr>'
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
		HistoryFrom: "Feb 1, 2026", HistoryTo: "Jun 1, 2026",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading with an explicit range: %v", err)
	}

	res, err := parseBankOutput(out, "Assets:Bank:RBC", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 1 {
		t.Fatalf("got %d transactions, want 1: %q", len(res.Transactions), out)
	}
	if res.Transactions[0].Description != "Feb 1, 2026" {
		t.Errorf("the from date typed into the filter = %q, want \"Feb 1, 2026\"", res.Transactions[0].Description)
	}
}
