package source

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// PC Financial's sign-in form renders inside a ThreatMetrix iframe (#tmx_tags_iframe): a Username and
// Password field on the first step, then a "Choose a verification method" select whose SMS option
// texts a code the person enters in the headed window. signIn must reach across the iframe to fill the
// two fields, submit, then select SMS on the step that follows -- the code itself is the person's to
// type and is never handled here. The fixture clears the sign-in wall only after the username and
// password are filled and SMS is chosen, so the test passes only if that whole cross-frame flow
// actually ran.
func TestPCFinancialSignsInAcrossTheThreatMetrixFrame(t *testing.T) {
	requireBrowserTests(t)
	const loginHTML = `<!doctype html><html><body>
	  <input aria-label="Username" name="username" type="text">
	  <input aria-label="Password" name="password" type="password">
	  <button type="submit" id="signin">Sign in</button>
	  <script>
	    document.getElementById('signin').addEventListener('click', function () {
	      if (document.querySelector('[name=username]').value &&
	          document.querySelector('[name=password]').value) {
	        location.href = '/verify'
	      }
	    })
	  </script>
	</body></html>`

	// The second step: choosing SMS is what confirms the sign-in (and, live, texts the code).
	const verifyHTML = `<!doctype html><html><body>
	  <label for="vm">Choose a verification method</label>
	  <select id="vm" aria-label="Choose a verification method">
	    <option value="">Select...</option>
	    <option value="ChallengeSMS">Text message</option>
	  </select>
	  <script>
	    document.getElementById('vm').addEventListener('change', function () {
	      if (this.value === 'ChallengeSMS') window.parent.postMessage('signed-in', '*')
	    })
	  </script>
	</body></html>`

	// The top page hosts the ThreatMetrix login iframe; a successful sign-in reveals the authenticated
	// app shell (<authenticated-header>, the 2026 redesign's element), the landmark signIn waits for.
	const topHTML = `<!doctype html><html><body>
	  <iframe id="tmx_tags_iframe" src="/login" style="width:400px;height:300px;border:0"></iframe>
	  <div id="app"></div>
	  <script>
	    window.addEventListener('message', function (e) {
	      if (e.data === 'signed-in') {
	        var f = document.getElementById('tmx_tags_iframe'); if (f) f.remove()
	        document.getElementById('app').innerHTML = '<authenticated-header><header><nav>Accounts</nav></header></authenticated-header>'
	      }
	    })
	  </script>
	</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write([]byte(loginHTML))
		case "/verify":
			_, _ = w.Write([]byte(verifyHTML))
		default:
			_, _ = w.Write([]byte(topHTML))
		}
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution: "pcfinancial", LoginURL: srv.URL, DefaultCurrency: "CAD",
		Account: "Liabilities:Personal:PC Mastercard",
	}, map[string]string{"username": "testuser", "password": "hunter2"})
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("signing in across the ThreatMetrix frame: %v", err)
	}

	// The transaction reader is not exercised here (that is TestPCFinancialReadsTheMastercard); this
	// test's point is that the cross-frame credentialed sign-in completes without error.
	if _, err := parseBankOutput(out, "test-connector", "Liabilities:Personal:PC Mastercard", "CAD"); err != nil {
		t.Fatalf("parsing pcfinancial.js output %q: %v", out, err)
	}
}

// pcfSignedIn wraps a PC Financial account body with the signed-in marker (no Username field anywhere)
// so isLoginWall reports false and the reader runs directly.
func pcfSignedIn(body string) string {
	return `<!doctype html><html><body><app-root>` + body + `</app-root></body></html>`
}

// pcfBalanceAndTable wraps rows in the Current balance tile and the transactions table, the shape of
// a card page with no pending section.
func pcfBalanceAndTable(rows string) string {
	return `
	  <balance-block><div class="balance-block transactions-lrg dollar"><div class="balance-block-content">
	    <div class="description-container"><p class="description tooltip">Current balance</p></div>
	    <div class="amount-container"><p class="amount"> $9,999.99 </p></div>
	  </div></div></balance-block>
	  <sortable-table amounttype="credit"><div class="table-sortable"><table><thead><tr><th>Description</th></tr></thead>
	  <tbody class="credit">` + rows + `</tbody></table></div></sortable-table>`
}

// pcfRow builds one PC Financial transaction row. amountClass is "positive" for a charge (money owed,
// which the books hold negative) or "negative" for a payment/refund (which the books hold positive).
func pcfRow(desc, date, amount, amountClass string) string {
	return `<tr class="clickable">
	  <td class="description"><div role="button"><sortable-table-cell-description><div class="description-cell-container">
	    <p class="description"><span class="description-text">` + desc + ` </span><span class="description-date">` + date + `</span><span class="description-date">9:09 AM</span></p>
	  </div></sortable-table-cell-description></div></td>
	  <td class="string">Purchase</td>
	  <td class="date"><span>` + date + `</span><span>9:09 AM</span></td>
	  <td class="amount ` + amountClass + `"> ` + amount + ` </td>
	  <td class="chevron"></td>
	</tr>`
}

// PC Financial's Mastercard renders its posted transactions in a sortable-table: a charge is tagged
// amount "positive" (it increases the balance owing) and a payment/refund amount "negative". The books
// hold a Mastercard negative, so the reader must flip that -- a charge is a negative amount, a payment
// positive -- and read the "Current balance" tile as the owing magnitude. This pins those against the
// real account DOM.
func TestPCFinancialReadsTheMastercard(t *testing.T) {
	requireBrowserTests(t)
	body := `
	  <balance-block><div class="balance-block transactions-lrg dollar"><div class="balance-block-content">
	    <div class="description-container"><p class="description tooltip">Current balance</p></div>
	    <div class="amount-container"><p class="amount"> $15,856.85 </p></div>
	  </div></div></balance-block>
	  <balance-block><div class="balance-block transactions-sml dollar"><div class="balance-block-content">
	    <div class="description-container"><p class="description tooltip">Available credit</p></div>
	    <div class="amount-container"><p class="amount"> $2,346.33 </p></div>
	  </div></div></balance-block>
	  <div class="table-body"><h2>Posted transactions</h2>
	  <sortable-table amounttype="credit"><div class="table-sortable"><table><thead><tr><th>Description</th></tr></thead>
	  <tbody class="credit">` +
		pcfRow("KENT SPRINGFIELD", "May 20, 2026", "$53.48", "positive") +
		pcfRow("Payment RBC", "May 8, 2026", "$16,867.02", "negative") +
		pcfRow("PURCHASE INTEREST CHARGE", "May 21, 2026", "$151.16", "positive") +
		`</tbody></table></div></sortable-table></div>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pcfSignedIn(body)))
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution: "pcfinancial", LoginURL: srv.URL, DefaultCurrency: "CAD",
		Account: "Liabilities:Personal:PC Mastercard",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading the PC Financial Mastercard: %v", err)
	}

	res, err := parseBankOutput(out, "test-connector", "Liabilities:Personal:PC Mastercard", "CAD")
	if err != nil {
		t.Fatalf("parsing pcfinancial.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 3 {
		t.Fatalf("got %d transactions, want 3: %q", len(res.Transactions), out)
	}
	// A charge (PCF "positive") is money owed -> negative in the books.
	if res.Transactions[0].Date.Format("2006-01-02") != "2026-05-20" ||
		res.Transactions[0].Description != "KENT SPRINGFIELD" ||
		res.Transactions[0].Amount.String() != "-53.48 CAD" {
		t.Errorf("charge row = %+v", res.Transactions[0])
	}
	// A payment (PCF "negative") reduces what is owed -> positive in the books.
	if res.Transactions[1].Description != "Payment RBC" || res.Transactions[1].Amount.String() != "16867.02 CAD" {
		t.Errorf("payment row = %+v", res.Transactions[1])
	}
	if res.Transactions[2].Description != "PURCHASE INTEREST CHARGE" || res.Transactions[2].Amount.String() != "-151.16 CAD" {
		t.Errorf("interest row = %+v", res.Transactions[2])
	}
	// The "Current balance" tile is the owing magnitude; the CLI negates it for the liability.
	if !res.HasBalance || res.Balance.String() != "15856.85 CAD" {
		t.Errorf("balance = %s (has=%v), want 15856.85 CAD", res.Balance, res.HasBalance)
	}
}

// A history window pages back through PC Financial's newest-first list until it passes the from-date,
// then keeps only rows on/after it. The fixture puts an older row on page 2 (revealed by Next), so the
// test passes only if the reader both advanced the paginator and dropped the out-of-window row.
func TestPCFinancialPagesBackToTheFromDate(t *testing.T) {
	requireBrowserTests(t)
	page1 := pcfRow("RECENT BUY", "Jul 10, 2026", "$40.00", "positive") +
		pcfRow("Payment RBC", "Jul 5, 2026", "$500.00", "negative")
	page2 := pcfRow("OLD BUY", "Feb 5, 2026", "$88.00", "positive")

	body := `
	  <balance-block><div class="balance-block transactions-lrg dollar"><div class="balance-block-content">
	    <div class="description-container"><p class="description tooltip">Current balance</p></div>
	    <div class="amount-container"><p class="amount"> $9,999.99 </p></div>
	  </div></div></balance-block>
	  <sortable-table amounttype="credit"><div class="table-sortable"><table><thead><tr><th>Description</th></tr></thead>
	  <tbody id="tb" class="credit">` + page1 + `</tbody></table></div></sortable-table>
	  <paginator><nav aria-label="Pagination"><ul>
	    <li class="page-number active"><button>1</button></li>
	    <li class="page-number"><button>2</button></li>
	    <li class="next-button"><button id="next">Next</button></li>
	  </ul></nav></paginator>
	  <script>
	    var pg = 1
	    document.getElementById('next').addEventListener('click', function () {
	      if (pg >= 2) return
	      pg = 2
	      document.getElementById('tb').innerHTML = ` + "`" + page2 + "`" + `
	      var lis = document.querySelectorAll('.page-number')
	      lis[0].classList.remove('active'); lis[1].classList.add('active')
	    })
	  </script>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pcfSignedIn(body)))
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution: "pcfinancial", LoginURL: srv.URL, DefaultCurrency: "CAD",
		Account:     "Liabilities:Personal:PC Mastercard",
		HistoryFrom: "Mar 1, 2026",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading with a PC Financial history window: %v", err)
	}

	res, err := parseBankOutput(out, "test-connector", "Liabilities:Personal:PC Mastercard", "CAD")
	if err != nil {
		t.Fatalf("parsing pcfinancial.js output %q: %v", out, err)
	}
	// Both page-1 rows are on/after Mar 1; the page-2 Feb row is older and must be dropped.
	if len(res.Transactions) != 2 {
		t.Fatalf("got %d transactions, want 2 (page-1 rows within the window): %q", len(res.Transactions), out)
	}
	for _, tx := range res.Transactions {
		if tx.Description == "OLD BUY" {
			t.Errorf("the out-of-window Feb row should have been dropped: %+v", tx)
		}
	}
}

// A charge dated exactly on the from-date belongs to the window. It was being dropped: the window is
// given as "Jul 16, 2026", which Date parses as LOCAL midnight, while a row's "2026-07-16" is an ISO
// date-only string, which Date parses as UTC midnight -- so west of Greenwich every row on the
// boundary day sorted before the boundary and was filtered out. On the real books this silently lost
// three PC Mastercard charges totalling 4,587.81, while the run reported a clean seam, and the
// documented way to seam an import (start at the last import's date) is exactly the case it breaks.
func TestPCFinancialKeepsChargesDatedOnTheFromDate(t *testing.T) {
	requireBrowserTests(t)
	t.Setenv("TZ", "America/Halifax") // any zone west of UTC reproduces it; the books' own zone

	body := pcfBalanceAndTable(
		pcfRow("BOUNDARY BUY", "Jul 16, 2026", "$3,624.17", "positive") +
			pcfRow("LATER BUY", "Jul 20, 2026", "$78.63", "positive"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pcfSignedIn(body)))
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution: "pcfinancial", LoginURL: srv.URL, DefaultCurrency: "CAD",
		Account:     "Liabilities:Personal:PC Mastercard",
		HistoryFrom: "Jul 16, 2026",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading with a from-date on a charge's own day: %v", err)
	}

	res, err := parseBankOutput(out, "test-connector", "Liabilities:Personal:PC Mastercard", "CAD")
	if err != nil {
		t.Fatalf("parsing pcfinancial.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 2 {
		t.Fatalf("got %d transactions, want 2 -- the charge dated on the from-date was dropped: %q",
			len(res.Transactions), out)
	}
}

// PC Financial shows a charge twice while it settles: once under Pending and again under Posted
// transactions. Reading both booked each one twice -- on the real books two Costco returns of 172.50
// arrived as four credits, over-crediting the card by 345.00. Only the posted section is a statement
// fact; a pending row posts within days and imports itself then, and the Current balance tile the
// reconciliation anchors on counts posted only, so reading pending puts the rows and the anchor at
// odds.
func TestPCFinancialReadsOnlyThePostedSection(t *testing.T) {
	requireBrowserTests(t)

	pending := `<sortable-table amounttype="credit"><div class="table-sortable"><table><thead><tr><th>Description</th></tr></thead>
	  <tbody class="credit">` +
		pcfRow("WWW COSTCO CA", "Jul 22, 2026", "$172.50", "negative") +
		pcfRow("WWW COSTCO CA", "Jul 22, 2026", "$172.50", "negative") +
		`</tbody></table></div></sortable-table>`

	posted := `<h2>Posted transactions</h2>
	  <sortable-table amounttype="credit"><div class="table-sortable"><table><thead><tr><th>Description</th></tr></thead>
	  <tbody class="credit">` +
		pcfRow("WWW COSTCO CA", "Jul 22, 2026", "$172.50", "negative") +
		pcfRow("WWW COSTCO CA", "Jul 22, 2026", "$172.50", "negative") +
		pcfRow("KENT SPRINGFIELD", "Jul 21, 2026", "$82.74", "positive") +
		`</tbody></table></div></sortable-table>`

	body := `
	  <balance-block><div class="balance-block transactions-lrg dollar"><div class="balance-block-content">
	    <div class="description-container"><p class="description tooltip">Current balance</p></div>
	    <div class="amount-container"><p class="amount"> $9,999.99 </p></div>
	  </div></div></balance-block>` + pending + posted

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pcfSignedIn(body)))
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution: "pcfinancial", LoginURL: srv.URL, DefaultCurrency: "CAD",
		Account: "Liabilities:Personal:PC Mastercard",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading a page with a pending section: %v", err)
	}

	res, err := parseBankOutput(out, "test-connector", "Liabilities:Personal:PC Mastercard", "CAD")
	if err != nil {
		t.Fatalf("parsing pcfinancial.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 3 {
		t.Fatalf("got %d transactions, want the 3 posted rows only -- the pending sightings were counted too: %q",
			len(res.Transactions), out)
	}
}

// Since the 2026 redesign a fresh sign-in lands on the dashboard, not the card, and the dashboard has
// no table -- only the top nav's "Transactions" link reaches it. The fixture serves the table only at
// /en/my/transactions, so the test passes only if the connector clicks through before reading.
func TestPCFinancialClicksThroughToTransactionsFromTheDashboard(t *testing.T) {
	requireBrowserTests(t)
	dashboard := pcfSignedIn(`<authenticated-header><nav><ul>
	  <li><a class="menu-item" href="/en/my/dashboard">Dashboard</a></li>
	  <li><a class="menu-item" href="/en/my/transactions">Transactions</a></li>
	</ul></nav></authenticated-header><h1>Welcome back</h1>`)
	card := pcfSignedIn(pcfBalanceAndTable(pcfRow("DIGITALOCEAN.COM", "Sep 1, 2026", "$23.62", "positive")))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/en/my/transactions" {
			_, _ = w.Write([]byte(card))
			return
		}
		_, _ = w.Write([]byte(dashboard))
	}))
	defer srv.Close()

	out, err := execBankScript(Bank{
		Institution: "pcfinancial", LoginURL: srv.URL + "/en/my/dashboard", DefaultCurrency: "CAD",
		Account: "Liabilities:Personal:PC Mastercard",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading the card from a dashboard landing: %v", err)
	}
	res, err := parseBankOutput(out, "test-connector", "Liabilities:Personal:PC Mastercard", "CAD")
	if err != nil {
		t.Fatalf("parsing pcfinancial.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 1 || res.Transactions[0].Amount.String() != "-23.62 CAD" {
		t.Fatalf("transactions = %+v, want the one card row: %q", res.Transactions, out)
	}
	if !res.HasBalance || res.Balance.String() != "9999.99 CAD" {
		t.Errorf("balance = %s (has=%v), want 9999.99 CAD", res.Balance, res.HasBalance)
	}
}
