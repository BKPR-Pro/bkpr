package source_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/lib/adapters/source"
	"github.com/dallasread/bookkeeper/lib/model"
)

func readLedger(t *testing.T, text string) []model.Transaction {
	t.Helper()
	txs, _, err := source.ReadLedger(strings.NewReader(text))
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	return txs
}

// A hand-kept file marks an entry the bank has not cleared with "!", not "*" -- an accrued invoice,
// an uncleared payment. That flag is real accounting state, so the reader carries it back on the
// entry rather than stripping it, and a cleared entry comes back not pending.
func TestReadLedgerCarriesThePendingFlag(t *testing.T) {
	_, entries, err := source.ReadLedger(strings.NewReader(`2026/04/01  ! (2073) DNSimple
  Income:Consulting:Contract:DNSimple  -9000.00 USD
  Assets:Consulting:Chequing

2026/04/02  * Swiss Chalet
  Expenses:Consulting:Meals  53.08 CAD
  Assets:Consulting:Chequing
`))
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if !entries[0].Pending {
		t.Errorf("the ! entry came back not pending, want its pending state carried")
	}
	if entries[0].Payee != "DNSimple" {
		t.Errorf("payee = %q, want the flag and code stripped off the name", entries[0].Payee)
	}
	if entries[0].Invoice != "2073" {
		t.Errorf("invoice = %q, want the (code) lifted into its own field", entries[0].Invoice)
	}
	if entries[1].Pending {
		t.Errorf("the * entry came back pending, want a cleared entry not pending")
	}
}

// The (code) in a header is the invoice or bill number: it is lifted out of the payee into its own
// field, so a number is data rather than text buried in a name, and a payee with no code keeps its
// whole name.
func TestReadLedgerLiftsTheInvoiceCode(t *testing.T) {
	_, entries, err := source.ReadLedger(strings.NewReader(`2026/04/01  * (2073) DNSimple
  Income:Consulting:Contract:DNSimple  -9000.00 USD
  Assets:Consulting:Chequing

2026/04/02  * Swiss Chalet
  Expenses:Consulting:Meals  53.08 CAD
  Assets:Consulting:Chequing
`))
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if entries[0].Invoice != "2073" || entries[0].Payee != "DNSimple" {
		t.Errorf("coded entry = {invoice %q, payee %q}, want {2073, DNSimple}", entries[0].Invoice, entries[0].Payee)
	}
	if entries[1].Invoice != "" || entries[1].Payee != "Swiss Chalet" {
		t.Errorf("uncoded entry = {invoice %q, payee %q}, want {\"\", Swiss Chalet}", entries[1].Invoice, entries[1].Payee)
	}
}

// The categorization the file already carries is read back alongside the line, so an import can
// assert it rather than making the rules re-derive what the file plainly says.
func TestReadLedgerCarriesTheCategorization(t *testing.T) {
	txs, entries, err := source.ReadLedger(strings.NewReader(`2026/03/01  * Acme Hardware
  Expenses:Materials  84.20 CAD
  Assets:Bank:Chequing
`))
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	got := entries[0]
	if len(got.Postings) != 1 || got.Postings[0].Account != "Expenses:Materials" {
		t.Fatalf("postings = %+v, want the file's Expenses:Materials", got.Postings)
	}
	if got.Postings[0].Amount.String() != "84.20 CAD" {
		t.Errorf("amount = %q, want 84.20 CAD, the file's categorized side", got.Postings[0].Amount)
	}
	if got.Payee != "Acme Hardware" {
		t.Errorf("payee = %q, want Acme Hardware", got.Payee)
	}
	// The carried categorization must account for the reconstructed line, or it could not be asserted.
	if !got.Balances(txs[0]) {
		t.Error("the carried categorization does not balance its line")
	}
}

// A split entry carries every categorized posting, not just the first.
func TestReadLedgerCarriesEveryLegOfASplit(t *testing.T) {
	_, entries, err := source.ReadLedger(strings.NewReader(`2026/03/05  * Hardware
  Expenses:A  10.00 CAD
  Expenses:B  15.00 CAD
  Assets:Bank:Chequing
`))
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(entries[0].Postings) != 2 {
		t.Fatalf("postings = %+v, want both legs of the split", entries[0].Postings)
	}
	if entries[0].Postings[0].Account != "Expenses:A" || entries[0].Postings[1].Account != "Expenses:B" {
		t.Errorf("postings = %+v, want Expenses:A and Expenses:B", entries[0].Postings)
	}
}

// When every posting is priced, the source account is the last one; the categorization it carries is
// every other posting, not the source itself.
func TestReadLedgerCategorizationExcludesTheSourceWhenFullyPriced(t *testing.T) {
	txs, entries, err := source.ReadLedger(strings.NewReader(`2026/03/01  * X
  Expenses:A  10.00 CAD
  Assets:Bank:Chequing  -10.00 CAD
`))
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(entries[0].Postings) != 1 || entries[0].Postings[0].Account != "Expenses:A" {
		t.Fatalf("postings = %+v, want only Expenses:A, not the source account", entries[0].Postings)
	}
	if !entries[0].Balances(txs[0]) {
		t.Error("the carried categorization does not balance its line")
	}
}

// The statement line is reconstructed from the entry: the account it came from is the single posting
// with no amount, and the line's amount is the negation of what the categorized postings account
// for, exactly as the writer elided it.
func TestReadLedgerReconstructsAnExpenseLine(t *testing.T) {
	txs := readLedger(t, `2026/03/01  * Acme Hardware
  Expenses:Materials  84.20 CAD
  Assets:Bank:Chequing
`)
	if len(txs) != 1 {
		t.Fatalf("got %d transactions, want 1", len(txs))
	}
	tx := txs[0]
	if tx.Account != "Assets:Bank:Chequing" {
		t.Errorf("account = %q, want the amountless posting", tx.Account)
	}
	if tx.Amount.String() != "-84.20 CAD" {
		t.Errorf("amount = %q, want -84.20 CAD (the negated categorized side)", tx.Amount)
	}
	if tx.Date.Format("2006-01-02") != "2026-03-01" || tx.Description != "Acme Hardware" {
		t.Errorf("date/desc = %q / %q", tx.Date.Format("2006-01-02"), tx.Description)
	}
	if tx.ID == "" {
		t.Error("no fingerprint")
	}
}

// A deposit: the income posting is negative, so the reconstructed statement line is positive.
func TestReadLedgerReconstructsADeposit(t *testing.T) {
	txs := readLedger(t, `2026/03/03  * Rent
  Income:Rent  -168.00 CAD
  Assets:Bank:Chequing
`)
	if txs[0].Amount.String() != "168.00 CAD" {
		t.Errorf("amount = %q, want 168.00 CAD", txs[0].Amount)
	}
}

// A split entry sums its categorized postings before negating.
func TestReadLedgerSumsASplit(t *testing.T) {
	txs := readLedger(t, `2026/03/05  * Hardware
  Expenses:A  10.00 CAD
  Expenses:B  15.00 CAD
  Assets:Bank:Chequing
`)
	if txs[0].Amount.String() != "-25.00 CAD" {
		t.Errorf("amount = %q, want -25.00 CAD", txs[0].Amount)
	}
}

// Entries are separated by a blank line, and comments are ignored.
func TestReadLedgerReadsSeveralEntriesAndSkipsComments(t *testing.T) {
	txs := readLedger(t, `; opening balance stuff
2026/03/01  * One
  Expenses:A  1.00 CAD
  Assets:Bank:Chequing

2026/03/02  * Two
  Expenses:B  2.00 CAD
  Assets:Bank:Chequing
`)
	if len(txs) != 2 || txs[0].Description != "One" || txs[1].Description != "Two" {
		t.Fatalf("got %+v, want two entries One and Two", txs)
	}
}

// Two identical entries are two lines, so their fingerprints are made distinct rather than colliding.
func TestReadLedgerKeepsIdenticalEntriesDistinct(t *testing.T) {
	txs := readLedger(t, `2026/03/01  * Coffee
  Expenses:Meals  5.00 CAD
  Assets:Bank:Chequing

2026/03/01  * Coffee
  Expenses:Meals  5.00 CAD
  Assets:Bank:Chequing
`)
	if len(txs) != 2 || txs[0].ID == txs[1].ID {
		t.Fatalf("ids = %q, %q; want two distinct", txs[0].ID, txs[1].ID)
	}
}

// A hand-written entry may price every posting. When it balances, the last posting is the
// statement account, following ledger's convention of writing the source account last.
func TestReadLedgerTakesTheLastPostingAsStatementWhenFullyPriced(t *testing.T) {
	txs := readLedger(t, `2026/03/01  * X
  Expenses:A  10.00 CAD
  Assets:Bank:Chequing  -10.00 CAD
`)
	tx := txs[0]
	if tx.Account != "Assets:Bank:Chequing" || tx.Amount.String() != "-10.00 CAD" {
		t.Errorf("got %q %q, want the last posting as the statement account", tx.Account, tx.Amount)
	}
}

// A fully priced entry that does not sum to zero is a broken book, refused rather than guessed.
func TestReadLedgerRefusesAFullyPricedEntryThatDoesNotBalance(t *testing.T) {
	_, _, err := source.ReadLedger(strings.NewReader(`2026/03/01  * X
  Expenses:A  10.00 CAD
  Assets:Bank:Chequing  -9.00 CAD
`))
	if err == nil {
		t.Fatal("a fully priced entry that does not balance; want an error")
	}
}

// A comment on the entry header has no posting to belong to, so it is dropped: the payee and the
// reconstructed amount ignore it entirely.
func TestReadLedgerDropsAHeaderComment(t *testing.T) {
	txs := readLedger(t, `2026/03/01  * Acme Hardware  ; paid in person
  Expenses:Materials  84.20 CAD
  Assets:Bank:Chequing
`)
	tx := txs[0]
	if tx.Description != "Acme Hardware" {
		t.Errorf("description = %q, want the header comment dropped", tx.Description)
	}
	if tx.Amount.String() != "-84.20 CAD" {
		t.Errorf("amount = %q, want -84.20 CAD", tx.Amount)
	}
}

// An inline note on a posting is that leg's comment, kept as data rather than dropped: the amount
// still parses cleanly with the note split off, and the note lands on the posting it followed.
func TestReadLedgerCarriesAPostingComment(t *testing.T) {
	_, entries, err := source.ReadLedger(strings.NewReader(`2026/03/01  * Acme Hardware
  Expenses:Materials  84.20 CAD  ; two boxes of screws
  Assets:Bank:Chequing
`))
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(entries) != 1 || len(entries[0].Postings) != 1 {
		t.Fatalf("got %+v, want one entry with one posting", entries)
	}
	p := entries[0].Postings[0]
	if p.Amount.String() != "84.20 CAD" {
		t.Errorf("amount = %q, want 84.20 CAD with the note split off", p.Amount)
	}
	if p.Comment != "two boxes of screws" {
		t.Errorf("comment = %q, want the inline note carried onto the posting", p.Comment)
	}
}

// A real ledger file opens with account directives and periodic (~) templates. They are not
// statement lines, so they are skipped, sub-lines and all.
func TestReadLedgerSkipsDirectivesAndPeriodicEntries(t *testing.T) {
	txs := readLedger(t, `account Assets:Bank:Chequing
  address 90 King Street
  address St. Stephen

~ Monthly
  Income:Rent  -1300.00 CAD
  Assets:Bank:Chequing

2026/03/01  * One
  Expenses:A  1.00 CAD
  Assets:Bank:Chequing
`)
	if len(txs) != 1 || txs[0].Description != "One" {
		t.Fatalf("got %+v, want just the dated entry", txs)
	}
}

// A commodity whose postings cancel among themselves (a unit placeholder moved between accounts)
// needs no price: only a commodity that leaves a remainder must be the statement line's own.
func TestReadLedgerDropsASelfBalancingCommodity(t *testing.T) {
	txs := readLedger(t, `2026/03/01  * Purchase
  Equity:Prop  -1 Property
  Assets:Prop  1 Property
  Expenses:Legal  100.00 CAD
  Assets:Bank:Chequing
`)
	tx := txs[0]
	if tx.Account != "Assets:Bank:Chequing" || tx.Amount.String() != "-100.00 CAD" {
		t.Errorf("got %q %q, want the CAD remainder on the elided account", tx.Account, tx.Amount)
	}
}

// Two commodities that both leave a remainder cannot be summed onto one statement line.
func TestReadLedgerRefusesTwoUnbalancedCommodities(t *testing.T) {
	_, _, err := source.ReadLedger(strings.NewReader(`2026/03/01  * X
  Income:Contract  -9000.00 USD
  Expenses:Fees  10.00 CAD
  Assets:Bank:Chequing
`))
	if err == nil {
		t.Fatal("two commodities with remainders; want an error")
	}
}

func TestReadLedgerRefusesTwoElidedAccounts(t *testing.T) {
	_, _, err := source.ReadLedger(strings.NewReader(`2026/03/01  * X
  Expenses:A  10.00 CAD
  Assets:Bank:Chequing
  Assets:Bank:Savings
`))
	if err == nil {
		t.Fatal("two amountless postings are ambiguous; want an error")
	}
}

// The writer notes the line's raw description as `; memo:` when a rule renamed the payee; reading
// it back is what makes an exported ledger regenerate the same fingerprints, so rules keyed on
// the description fire the same on a re-import.
func TestReadLedgerPrefersTheMemoNoteOverThePayee(t *testing.T) {
	txs, _, err := source.ReadLedger(strings.NewReader(`2026/03/02  * Acme Hardware
  ; memo: ACME HARDWARE #4471
  Expenses:Repairs:Materials  84.20 CAD
  Assets:Bank:Chequing
`))
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(txs) != 1 {
		t.Fatalf("got %d transactions, want 1", len(txs))
	}
	if txs[0].Description != "ACME HARDWARE #4471" {
		t.Errorf("description = %q, want the memo, not the payee", txs[0].Description)
	}
}

// A posting priced with "@@" — the very form the ledger writer emits for a cross-commodity leg —
// reads back carrying its cost, so a book bookkeeper wrote re-imports instead of being refused.
func TestReadLedgerReadsAnAtAtPricedPosting(t *testing.T) {
	txs, entries, err := source.ReadLedger(strings.NewReader(`2026/03/01  * Broker
  Assets:Brokerage:AAPL  10 AAPL @@ 1000.00 USD
  Assets:Bank  -1000.00 USD
`))
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(txs) != 1 || len(entries) != 1 {
		t.Fatalf("got %d txs / %d entries, want 1 each", len(txs), len(entries))
	}
	if len(entries[0].Postings) != 1 {
		t.Fatalf("postings = %+v, want the single AAPL leg", entries[0].Postings)
	}
	got := entries[0].Postings[0]
	if got.Account != "Assets:Brokerage:AAPL" || got.Amount.String() != "10 AAPL" {
		t.Errorf("posting = %+v, want 10 AAPL at Assets:Brokerage:AAPL", got)
	}
	if got.Cost == nil || got.Cost.String() != "1000.00 USD" {
		t.Errorf("cost = %v, want 1000.00 USD carried from the @@ price", got.Cost)
	}
	// The share leg is valued through its cost, so the line balances against the cash it took.
	if !entries[0].Balances(txs[0]) {
		t.Error("the priced categorization does not balance its line")
	}
}

// A property purchase mixes a Property commodity (its two legs cancelling) with a cash split. Priced
// with @@, the Property legs cancel in cash too, so the entry balances on the financing alone and
// nothing is left uncategorized — the round trip that a bare commodity unit could not make.
func TestReadLedgerRoundTripsAPricedPropertyPurchase(t *testing.T) {
	txs, entries, err := source.ReadLedger(strings.NewReader(`2024/10/10  * Vendor
  Equity:Real Estate:9 Schoodic Street  -1 Property @@ 50000.00 CAD
  Assets:Real Estate:9 Schoodic Street   1 Property @@ 50000.00 CAD
  Expenses:Real Estate:Legal:9 Schoodic Street  5000.00 CAD
  Liabilities:Simplii LOC:9 Schoodic Street
`))
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	tx := txs[0]
	if tx.Account != "Liabilities:Simplii LOC:9 Schoodic Street" {
		t.Errorf("source account = %q, want the amountless Simplii LOC plug", tx.Account)
	}
	if tx.Amount.String() != "-5000.00 CAD" {
		t.Errorf("line amount = %q, want -5000.00 CAD (the financing the split did not cover)", tx.Amount)
	}
	if len(entries[0].Postings) != 3 {
		t.Fatalf("postings = %+v, want both Property legs and the Legal leg", entries[0].Postings)
	}
	var pricedLegs int
	for _, p := range entries[0].Postings {
		if p.Amount.Commodity == "Property" {
			if p.Cost == nil || p.Cost.String() != "50000.00 CAD" {
				t.Errorf("Property leg %q has cost %v, want 50000.00 CAD", p.Account, p.Cost)
			}
			pricedLegs++
		}
	}
	if pricedLegs != 2 {
		t.Errorf("carried %d priced Property legs, want 2", pricedLegs)
	}
	if entries[0].Uncategorized() {
		t.Error("a fully priced purchase left an uncategorized leg")
	}
	if !entries[0].Balances(tx) {
		t.Error("the carried categorization does not balance its line")
	}
}

// A per-unit "@" is refused on read exactly as it is on the -post path: a total is the exact cash
// paid, a per-unit price is not.
func TestReadLedgerRefusesAPerUnitPrice(t *testing.T) {
	_, _, err := source.ReadLedger(strings.NewReader(`2026/03/01  * Broker
  Assets:Brokerage:AAPL  10 AAPL @ 100.00 USD
  Assets:Bank  -1000.00 USD
`))
	if err == nil {
		t.Fatal("a per-unit @ price should be refused on read")
	}
}
