package source

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A card (RBC Avion Visa) is on the same Angular grid as the chequing, but two things differ: its date
// cell is tagged cc-date-large (both still carry date-column-padding), and RBC signs a card by effect
// on the owing balance -- a charge in Withdrawals is positive, a payment in Deposits is negative, the
// reverse of a chequing. As a liability the books hold it negative, so a charge must come out negative
// and a payment positive.
func TestRBCReadsAVisaCard(t *testing.T) {
	const page = `<!doctype html><html><body>
	  <table class="rbc-transaction-list-table"><tbody>
	    <tr data-role="transaction-list-table-transaction" class="rbc-transaction-list-transaction-new">
	      <td class="cc-date-large date-column-padding" headers="cc-date-large" id="2026-07-01"> Jul 1, 2026 </td>
	      <td class="rbc-transaction-list-desc description-column-padding" headers="2026-07-01"><div> ANNUAL FEE </div></td>
	      <td class="rbc-transaction-list-withdraw"><span>$175.00</span></td>
	      <td class="rbc-transaction-list-balance"> $175.00 </td>
	    </tr>
	    <tr data-role="transaction-list-table-transaction" class="rbc-transaction-list-transaction-new">
	      <td class="cc-date-large date-column-padding" headers="cc-date-large" id="2026-07-07"> Jul 7, 2026 </td>
	      <td class="rbc-transaction-list-desc description-column-padding" headers="2026-07-07"><div> PAYMENT - THANK YOU </div></td>
	      <td class="rbc-transaction-list-deposit"><span>-$175.00</span></td>
	      <td class="rbc-transaction-list-balance">  </td>
	    </tr>
	  </tbody></table>
	</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	// Account is a liability, so the script flips the card's signs.
	out, err := execBankScript(Bank{
		Institution: "rbc", LoginURL: srv.URL, DefaultCurrency: "CAD",
		Account: "Liabilities:Consulting:RBC Visa",
	}, nil)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("reading the Visa: %v", err)
	}

	res, err := parseBankOutput(out, "Liabilities:Consulting:RBC Visa", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 2 {
		t.Fatalf("got %d transactions, want 2: %q", len(res.Transactions), out)
	}
	// A charge increases what is owed -> negative in the books.
	if res.Transactions[0].Date.Format("2006-01-02") != "2026-07-01" ||
		res.Transactions[0].Description != "ANNUAL FEE" ||
		res.Transactions[0].Amount.String() != "-175.00 CAD" {
		t.Errorf("charge row = %+v", res.Transactions[0])
	}
	// A payment reduces what is owed -> positive.
	if res.Transactions[1].Date.Format("2006-01-02") != "2026-07-07" ||
		res.Transactions[1].Amount.String() != "175.00 CAD" {
		t.Errorf("payment row = %+v", res.Transactions[1])
	}
}
