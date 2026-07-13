package source

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// With a history window, rbc.js drives RBC's custom date-range filter: it fills the from/to date
// fields (#rbc-dp-0 / #rbc-dp-1), runs Search, and pages through "Show More" so the whole range loads.
// Here the fixture shows only recent rows until a from-date is entered and Search is clicked, then a
// "Show More" reveals the rest -- so the test passes only if all three happened.
func TestRBCReadsAWiderHistoryWindow(t *testing.T) {
	const page = `<!doctype html><html><body>
	  <label>From</label><input id="rbc-dp-0" type="text" placeholder="Select...">
	  <label>To</label><input id="rbc-dp-1" type="text" placeholder="Select...">
	  <button id="search" type="submit">Search</button>
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
	    // Search with a from-date reveals a Show More button.
	    document.getElementById('search').addEventListener('click', function () {
	      if (document.getElementById('rbc-dp-0').value.trim() !== '') {
	        document.getElementById('more').style.display = 'inline'
	      }
	    })
	    // Show More appends an older transaction, then hides itself.
	    document.getElementById('more').addEventListener('click', function () {
	      document.getElementById('tb').insertAdjacentHTML('beforeend',
	        '<tr data-role="transaction-list-table-transaction" class="rbc-transaction-list-transaction-new">' +
	        '<td headers="date" id="2026-04-02"> Apr 2, 2026 </td>' +
	        '<td class="rbc-transaction-list-desc"><div> Older </div></td>' +
	        '<td class="rbc-transaction-list-deposit"><span>$2.00</span></td>' +
	        '<td class="rbc-transaction-list-balance"> </td></tr>')
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
		Institution: "rbc", LoginURL: srv.URL, DefaultCurrency: "CAD", HistoryDays: 120,
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading with a history window: %v", err)
	}

	res, err := parseBankOutput(out, "Assets:Bank:RBC", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	// Two rows only if the from-date was entered, Search clicked, and Show More paged in the older one.
	if len(res.Transactions) != 2 {
		t.Fatalf("got %d transactions, want 2 (recent + older via Show More): %q", len(res.Transactions), out)
	}
	got := res.Transactions[0].Date.Format("2006-01-02") + "," + res.Transactions[1].Date.Format("2006-01-02")
	if !strings.Contains(got, "2026-04-02") {
		t.Errorf("older transaction not loaded; dates = %s", got)
	}
}
