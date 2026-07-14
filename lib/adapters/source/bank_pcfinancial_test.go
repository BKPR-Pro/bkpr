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
	// app shell (<app-auth-header>), the landmark signIn waits for.
	const topHTML = `<!doctype html><html><body>
	  <iframe id="tmx_tags_iframe" src="/login" style="width:400px;height:300px;border:0"></iframe>
	  <div id="app"></div>
	  <script>
	    window.addEventListener('message', function (e) {
	      if (e.data === 'signed-in') {
	        var f = document.getElementById('tmx_tags_iframe'); if (f) f.remove()
	        document.getElementById('app').innerHTML = '<app-auth-header><header><nav>Accounts</nav></header></app-auth-header>'
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
	}, map[string]string{"username": "dallas", "password": "hunter2"})
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("signing in across the ThreatMetrix frame: %v", err)
	}

	// The transaction reader is not exercised here (that is TestPCFinancialReadsTheMastercard); this
	// test's point is that the cross-frame credentialed sign-in completes without error.
	if _, err := parseBankOutput(out, "Liabilities:Personal:PC Mastercard", "CAD"); err != nil {
		t.Fatalf("parsing pcfinancial.js output %q: %v", out, err)
	}
}

// pcfSignedIn wraps a PC Financial account body with the signed-in marker (no Username field anywhere)
// so isLoginWall reports false and the reader runs directly.
func pcfSignedIn(body string) string {
	return `<!doctype html><html><body><app-root>` + body + `</app-root></body></html>`
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
		pcfRow("KENT ST.STEPHEN", "May 20, 2026", "$53.48", "positive") +
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

	res, err := parseBankOutput(out, "Liabilities:Personal:PC Mastercard", "CAD")
	if err != nil {
		t.Fatalf("parsing pcfinancial.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 3 {
		t.Fatalf("got %d transactions, want 3: %q", len(res.Transactions), out)
	}
	// A charge (PCF "positive") is money owed -> negative in the books.
	if res.Transactions[0].Date.Format("2006-01-02") != "2026-05-20" ||
		res.Transactions[0].Description != "KENT ST.STEPHEN" ||
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

	res, err := parseBankOutput(out, "Liabilities:Personal:PC Mastercard", "CAD")
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
