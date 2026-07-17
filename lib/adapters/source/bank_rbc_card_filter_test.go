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

	res, err := parseBankOutput(out, "test-connector", "Liabilities:Consulting:RBC Visa", "CAD")
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

// After Apply, RBC's Angular app renders the filtered page and its "Show More" control a moment later,
// and only the first page of rows loads until Show More is clicked through -- the connector returned
// "50 of 65". A presence check that runs the instant Apply settles races that render and stops at page
// one. applyDateRange must wait for Show More to (re)appear before deciding the range is fully loaded.
// Here the button is inserted 3s after Apply -- past applyDateRange's own post-Apply wait -- so the
// older row is reached only if the loop waits for it rather than breaking immediately.
func TestRBCCardPagesShowMoreThatRendersLate(t *testing.T) {
	requireBrowserTests(t)
	const page = `<!doctype html><html><body>
	  <div aria-label="Posted Transactions"><button id="kw" type="button">Search</button><button id="filter" type="button">Filter</button></div>
	  <div id="panel" style="display:none">
	    <label for="filterFrom" class="hidden">Date Range From</label><input id="filterFrom" type="text" placeholder="Select...">
	    <label for="filterTo" class="hidden">Date Range To</label><input id="filterTo" type="text" placeholder="Select...">
	    <button id="apply" type="button"><span>Apply</span></button>
	  </div>
	  <table class="rbc-transaction-list-table"><tbody id="tb"></tbody></table>
	  <div id="showmore"></div>
	  <script>
	    function row(id, date, desc, amt) {
	      return '<tr data-role="transaction-list-table-transaction" class="rbc-transaction-list-transaction-new">' +
	        '<td class="date-column-padding" headers="date" id="' + id + '"> ' + date + ' </td>' +
	        '<td class="rbc-transaction-list-desc"><div> ' + desc + ' </div></td>' +
	        '<td class="rbc-transaction-list-withdraw"><span>' + amt + '</span></td>' +
	        '<td class="rbc-transaction-list-balance"> ' + amt + ' </td></tr>'
	    }
	    document.getElementById('filter').addEventListener('click', function () {
	      document.getElementById('panel').style.display = 'block' })
	    document.getElementById('apply').addEventListener('click', function () {
	      if (document.getElementById('filterFrom').value.trim() === '' ||
	          document.getElementById('filterTo').value.trim() === '') return
	      document.getElementById('tb').innerHTML = row('2026-07-10', 'Jul 10, 2026', 'NEW CHARGE', '$10.00')
	      // The Show More control renders late, after applyDateRange's own post-Apply wait.
	      setTimeout(function () {
	        document.getElementById('showmore').innerHTML = '<button type="button" class="cc-view-more-button"> Show More </button>'
	        document.querySelector('.cc-view-more-button').addEventListener('click', function () {
	          document.getElementById('tb').innerHTML += row('2026-02-05', 'Feb 5, 2026', 'OLD CHARGE', '$40.00')
	          document.getElementById('showmore').innerHTML = ''
	        })
	      }, 3000)
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
		Account:     "Liabilities:Personal:RBC Cash Back Mastercard",
		HistoryFrom: "Feb 1, 2026", HistoryTo: "Jul 13, 2026",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("paging the late-rendering Show More: %v", err)
	}

	res, err := parseBankOutput(out, "test-connector", "Liabilities:Personal:RBC Cash Back Mastercard", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 2 {
		t.Fatalf("got %d transactions, want 2 (the older row loads only after Show More is paged): %q", len(res.Transactions), out)
	}
	// Newest first; the older row is the one the immediate presence check would have missed.
	if res.Transactions[1].Date.Format("2006-01-02") != "2026-02-05" || res.Transactions[1].Amount.String() != "-40.00 CAD" {
		t.Errorf("older row = %+v", res.Transactions[1])
	}
}

// The real Cash Back Mastercard page carries TWO date pickers: an unrelated "make a payment" widget
// whose "When" field is #rbc-dp-0 (and sorts first in the DOM), and the Posted-transactions Filter
// panel's own "Date Range From"/"Date Range To" fields. applyDateRange must type the history window
// into the Filter's own fields, not the payment field -- so here #rbc-dp-0 is a decoy whose value is
// ignored, and the older row is revealed only when the Filter's From and To are both set and Apply is
// clicked. With the buggy union-then-.first() selector the from date lands in the decoy, the filter's
// From stays empty, and no row appears -- so this fails until the selector prefers the labelled field.
func TestRBCCardFilterTargetsDateRangeNotPaymentField(t *testing.T) {
	requireBrowserTests(t)
	const page = `<!doctype html><html><body>
	  <!-- Decoy: a "make a payment" widget whose When date is #rbc-dp-0, first in the DOM. -->
	  <label for="rbc-dp-0">When</label><input id="rbc-dp-0" type="text">

	  <div aria-label="Posted Transactions">
	    <button id="kw" type="button">Search</button>
	    <button id="filter" type="button">Filter</button>
	  </div>
	  <div id="panel" style="display:none">
	    <label for="filterFrom" class="hidden">Date Range From</label><input id="filterFrom" type="text" placeholder="Select...">
	    <label for="filterTo" class="hidden">Date Range To</label><input id="filterTo" type="text" placeholder="Select...">
	    <button id="apply" type="button"><span>Apply</span></button>
	  </div>
	  <table class="rbc-transaction-list-table"><tbody id="tb"></tbody></table>
	  <script>
	    document.getElementById('filter').addEventListener('click', function () {
	      document.getElementById('panel').style.display = 'block' })
	    document.getElementById('apply').addEventListener('click', function () {
	      // Revealed only when the FILTER's own dates are set; the payment field is irrelevant.
	      if (document.getElementById('filterFrom').value.trim() !== '' &&
	          document.getElementById('filterTo').value.trim() !== '') {
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
		Account:     "Liabilities:Personal:RBC Cash Back Mastercard",
		HistoryFrom: "Feb 1, 2026", HistoryTo: "Jul 13, 2026",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("widening the card via its filter date range: %v", err)
	}

	res, err := parseBankOutput(out, "test-connector", "Liabilities:Personal:RBC Cash Back Mastercard", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 1 {
		t.Fatalf("got %d transactions, want 1 (the older charge, revealed only when the Filter's own From/To are set): %q", len(res.Transactions), out)
	}
	if res.Transactions[0].Date.Format("2006-01-02") != "2026-02-05" || res.Transactions[0].Amount.String() != "-40.00 CAD" {
		t.Errorf("row = %+v", res.Transactions[0])
	}
}
