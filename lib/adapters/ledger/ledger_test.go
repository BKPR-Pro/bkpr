package ledger_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dallasread/bookkeeper/lib/adapters/ledger"
	"github.com/dallasread/bookkeeper/lib/adapters/source"
	"github.com/dallasread/bookkeeper/lib/model"
)

func on(day int) time.Time {
	return time.Date(2026, 3, day, 0, 0, 0, 0, time.UTC)
}

func cad(cents int64) model.Amount { return model.Amount{Units: cents, Scale: 2, Commodity: "CAD"} }

func chequing(day int, cents int64, description string) model.Transaction {
	return model.Transaction{
		Date: on(day), Amount: cad(cents),
		Description: description, Account: "Assets:Bank:Chequing",
	}
}

func write(t *testing.T, tx model.Transaction, e model.Entry) string {
	t.Helper()
	var buf bytes.Buffer
	if err := ledger.WriteAll(&buf, []model.Transaction{tx}, []model.Entry{e}); err != nil {
		t.Fatalf("WriteAll: %v", err)
	}
	return buf.String()
}

// Money out of the source account debits the expense and credits the account, which is elided.
func TestExpenseEntry(t *testing.T) {
	tx := chequing(2, -8420, "ACME HARDWARE #4471")
	e := model.Entry{
		Payee:    "Acme Hardware",
		Postings: []model.Posting{{Account: "Expenses:Repairs:Materials", Amount: cad(8420)}},
	}

	want := "2026/03/02  * Acme Hardware\n" +
		"  ; memo: ACME HARDWARE #4471\n" +
		"  Expenses:Repairs:Materials  84.20 CAD\n" +
		"  Assets:Bank:Chequing\n"

	if got := write(t, tx, e); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A rule that renames the payee would otherwise cost the line its raw description, and with it
// the fingerprint and every rule keyed on it. The memo note keeps that fact in the artifact; when
// the title already is the description, there is nothing to note.
func TestTheMemoNoteAppearsOnlyWhenARuleRenamedThePayee(t *testing.T) {
	tx := chequing(12, -3999, "UNKNOWN MERCHANT 88")
	e := model.Entry{
		Payee:    "UNKNOWN MERCHANT 88",
		Postings: []model.Posting{{Account: model.Uncategorized, Amount: cad(3999)}},
	}

	if got := write(t, tx, e); strings.Contains(got, "; memo:") {
		t.Errorf("a payee identical to the description needs no memo:\n%s", got)
	}
}

// A posting's comment is rendered inline after the amount as an ordinary ledger note, so a person
// can leave a reason on one leg of a split. A commentless posting is unchanged, so nothing else drifts.
func TestPostingCommentIsWrittenInline(t *testing.T) {
	tx := chequing(2, -8420, "ACME HARDWARE #4471")
	e := model.Entry{
		Payee: "Acme Hardware",
		Postings: []model.Posting{
			{Account: "Expenses:Repairs:Materials", Amount: cad(6000), Comment: "lumber for the deck"},
			{Account: "Expenses:Repairs:Tools", Amount: cad(2420)},
		},
	}

	got := write(t, tx, e)

	if !strings.Contains(got, "  Expenses:Repairs:Materials  60.00 CAD  ; lumber for the deck\n") {
		t.Errorf("the commented leg should carry its note inline:\n%s", got)
	}
	if !strings.Contains(got, "  Expenses:Repairs:Tools  24.20 CAD\n") {
		t.Errorf("the uncommented leg should carry no note:\n%s", got)
	}
}

// A priced posting carries its note after the @@ cost, so the cost form and the comment coexist.
func TestPostingCommentFollowsTheCost(t *testing.T) {
	tx := model.Transaction{
		Date: on(1), Amount: model.Amount{Units: -100000, Scale: 2, Commodity: "USD"},
		Description: "BUY AAPL", Account: "Assets:Bank",
	}
	cost := model.Amount{Units: 100000, Scale: 2, Commodity: "USD"}
	e := model.Entry{
		Payee: "Broker",
		Postings: []model.Posting{
			{Account: "Assets:Brokerage:AAPL", Amount: model.Amount{Units: 10, Commodity: "AAPL"}, Cost: &cost, Comment: "opening lot"},
		},
	}

	if got := write(t, tx, e); !strings.Contains(got, "  Assets:Brokerage:AAPL  10 AAPL @@ 1000.00 USD  ; opening lot\n") {
		t.Errorf("the note should follow the @@ cost:\n%s", got)
	}
}

// Money into the source account credits income. The sign flips relative to the statement line.
func TestIncomeEntry(t *testing.T) {
	tx := chequing(5, 160000, "E-TRANSFER FROM J SMITH")
	e := model.Entry{
		Payee:    "J. Smith",
		Postings: []model.Posting{{Account: "Income:Real Estate:Rent:123 Example Street", Amount: cad(-160000)}},
	}

	if got := write(t, tx, e); !strings.Contains(got, "Income:Real Estate:Rent:123 Example Street  -1600.00 CAD") {
		t.Errorf("income posting should be negative:\n%s", got)
	}
}

// An entry's invoice or bill number is written as the ledger transaction code, "(2073)" before the
// payee, so a number set as its own field renders where ledger tools read a check/invoice number.
func TestInvoiceNumberRendersAsTheLedgerCode(t *testing.T) {
	tx := chequing(1, 900000, "DNSimple")
	e := model.Entry{
		Payee:    "DNSimple",
		Invoice:  "2073",
		Postings: []model.Posting{{Account: "Income:Consulting:Contract:DNSimple", Amount: cad(-900000)}},
	}

	got := write(t, tx, e)
	if !strings.Contains(got, "* (2073) DNSimple\n") {
		t.Errorf("header should carry the invoice as a (code):\n%s", got)
	}
}

// A line whose raw description is just its coded title -- the shape a hand-kept "(2073) DNSimple"
// import leaves once the code is lifted into its own field -- writes no memo, because the memo would
// only repeat the header. A description that differs for a real reason still shows.
func TestNoRedundantMemoForACodedTitle(t *testing.T) {
	tx := chequing(1, 900000, "(2073) DNSimple")
	e := model.Entry{
		Payee:    "DNSimple",
		Invoice:  "2073",
		Postings: []model.Posting{{Account: "Income:Consulting:Contract:DNSimple", Amount: cad(-900000)}},
	}
	if got := write(t, tx, e); strings.Contains(got, "; memo:") {
		t.Errorf("a description that is just the coded title should write no memo:\n%s", got)
	}

	e.Invoice, tx = "", chequing(1, 900000, "RAW BANK LINE 88")
	if got := write(t, tx, e); !strings.Contains(got, "; memo: RAW BANK LINE 88") {
		t.Errorf("a genuinely different description should still write a memo:\n%s", got)
	}
}

// Every line bookkeeper writes came off a bank statement, so every line has cleared the bank.
// The pending flag means the bank has not reported a transaction yet, and nothing here is that.
func TestEveryEntryIsCleared(t *testing.T) {
	tx := chequing(12, -3999, "UNKNOWN MERCHANT 88")
	e := model.Entry{
		Payee:    "UNKNOWN MERCHANT 88",
		Postings: []model.Posting{{Account: model.Uncategorized, Amount: cad(3999)}},
	}

	got := write(t, tx, e)

	if !strings.HasPrefix(got, "2026/03/12  * UNKNOWN MERCHANT 88\n") {
		t.Errorf("an imported line is cleared, whatever its account:\n%s", got)
	}
	if strings.Contains(got, "!") {
		t.Errorf("nothing imported from a statement is pending:\n%s", got)
	}
}

// A hand-kept file carries genuine pending state: an accrued invoice or an uncleared payment the
// bank has not reported yet is marked "!", not "*". That state is the writer's to keep, so a pending
// entry renders with the pending flag rather than being asserted cleared.
func TestAPendingEntryRendersPending(t *testing.T) {
	tx := chequing(1, -900000, "(2073) DNSimple")
	e := model.Entry{
		Payee:    "(2073) DNSimple",
		Pending:  true,
		Postings: []model.Posting{{Account: "Income:Consulting:Contract:DNSimple", Amount: cad(900000)}},
	}

	got := write(t, tx, e)

	if !strings.HasPrefix(got, "2026/03/01  ! (2073) DNSimple\n") {
		t.Errorf("a pending entry should render with the pending flag:\n%s", got)
	}
	if strings.Contains(got, "  * ") {
		t.Errorf("a pending entry must not be asserted cleared:\n%s", got)
	}
}

// The account path is the only marker of what is unknown. Nothing annotates it.
func TestAnUncategorizedLineCarriesNoAnnotation(t *testing.T) {
	tx := chequing(2, -8420, "ACME HARDWARE #4471")
	e := model.Entry{
		Payee:    "Acme Hardware",
		Postings: []model.Posting{{Account: "Expenses:Real Estate:Materials:Uncategorized", Amount: cad(8420)}},
	}

	want := "2026/03/02  * Acme Hardware\n" +
		"  ; memo: ACME HARDWARE #4471\n" +
		"  Expenses:Real Estate:Materials:Uncategorized  84.20 CAD\n" +
		"  Assets:Bank:Chequing\n"

	if got := write(t, tx, e); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// One charge, two properties. This is the whole reason postings replaced a single category: the
// true answer is often not one account, and it lives on a receipt rather than in the description.
func TestASplitWritesEveryPostingAndStillElidesTheSourceAccount(t *testing.T) {
	tx := chequing(2, -8420, "ACME HARDWARE #4471")
	e := model.Entry{
		Payee: "Acme Hardware",
		Postings: []model.Posting{
			{Account: "Expenses:Materials:Unit 1", Amount: cad(4000)},
			{Account: "Expenses:Materials:Unit 2", Amount: cad(4420)},
		},
	}

	want := "2026/03/02  * Acme Hardware\n" +
		"  ; memo: ACME HARDWARE #4471\n" +
		"  Expenses:Materials:Unit 1  40.00 CAD\n" +
		"  Expenses:Materials:Unit 2  44.20 CAD\n" +
		"  Assets:Bank:Chequing\n"

	if got := write(t, tx, e); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// The artifact is the books. An entry whose postings do not account for the whole statement line
// would write a silently wrong ledger, so it is refused rather than emitted.
func TestPostingsThatDoNotAccountForTheLineAreRefused(t *testing.T) {
	tx := chequing(2, -8420, "ACME HARDWARE #4471")
	e := model.Entry{
		Payee: "Acme Hardware",
		Postings: []model.Posting{
			{Account: "Expenses:Materials:Unit 1", Amount: cad(4000)},
			{Account: "Expenses:Materials:Unit 2", Amount: cad(400)},
		},
	}

	var buf bytes.Buffer
	err := ledger.WriteAll(&buf, []model.Transaction{tx}, []model.Entry{e})
	if err == nil {
		t.Fatalf("wrote an unbalanced entry:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "84.20") {
		t.Errorf("the error should say what the line was worth: %v", err)
	}
}

func TestEntriesAreSeparatedByBlankLines(t *testing.T) {
	txs := []model.Transaction{chequing(1, -100, "A"), chequing(2, -200, "B")}
	entries := []model.Entry{
		{Payee: "A", Postings: []model.Posting{{Account: "Expenses:X", Amount: cad(100)}}},
		{Payee: "B", Postings: []model.Posting{{Account: "Expenses:Y", Amount: cad(200)}}},
	}

	var buf bytes.Buffer
	if err := ledger.WriteAll(&buf, txs, entries); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(buf.String(), "Assets:Bank:Chequing\n\n2026/03/02") {
		t.Errorf("entries should be separated by a blank line:\n%s", buf.String())
	}
}

// Currency belongs to the account the statement came from, not to whoever ran the render. Two
// accounts in different currencies write different postings in one pass.
func TestEachEntryIsWrittenInItsOwnAccountsCurrency(t *testing.T) {
	usdAmount := model.Amount{Units: -100, Scale: 2, Commodity: "USD"}
	usd := model.Transaction{
		Date: on(1), Amount: usdAmount,
		Description: "B", Account: "Assets:Bank:Operating USD",
	}
	txs := []model.Transaction{chequing(1, -100, "A"), usd}
	entries := []model.Entry{
		{Payee: "A", Postings: []model.Posting{{Account: "Expenses:X", Amount: cad(100)}}},
		{Payee: "B", Postings: []model.Posting{{Account: "Expenses:X", Amount: usdAmount.Negate()}}},
	}

	var buf bytes.Buffer
	if err := ledger.WriteAll(&buf, txs, entries); err != nil {
		t.Fatal(err)
	}

	got := buf.String()
	if !strings.Contains(got, "1.00 CAD") || !strings.Contains(got, "1.00 USD") {
		t.Errorf("want one posting in each currency:\n%s", got)
	}
}

// Buying shares with cash is two commodities in one entry. The share posting carries its total
// price in the "@@" form ledger reads, so ledger can value the position and compute basis later.
func TestAPricedPostingIsWrittenWithItsTotalCost(t *testing.T) {
	usd := func(cents int64) model.Amount { return model.Amount{Units: cents, Scale: 2, Commodity: "USD"} }
	tx := model.Transaction{
		Date: on(1), Amount: usd(-100000), Description: "BOUGHT 10 AAPL",
		Account: "Assets:Brokerage:Cash",
	}
	cost := usd(100000)
	e := model.Entry{
		Payee:    "Bought Apple",
		Postings: []model.Posting{{Account: "Assets:Brokerage:AAPL", Amount: model.Amount{Units: 10, Commodity: "AAPL"}, Cost: &cost}},
	}

	want := "2026/03/01  * Bought Apple\n" +
		"  ; memo: BOUGHT 10 AAPL\n" +
		"  Assets:Brokerage:AAPL  10 AAPL @@ 1000.00 USD\n" +
		"  Assets:Brokerage:Cash\n"

	if got := write(t, tx, e); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// A commodity-less transaction would write "84.20 " with a trailing space, which is not a ledger.
func TestATransactionWithoutACommodityIsRefused(t *testing.T) {
	tx := chequing(1, -100, "A")
	tx.Amount.Commodity = ""
	e := model.Entry{Payee: "A", Postings: []model.Posting{{Account: "Expenses:X", Amount: cad(100)}}}

	if err := ledger.WriteAll(&bytes.Buffer{}, []model.Transaction{tx}, []model.Entry{e}); err == nil {
		t.Fatal("wrote an entry with no commodity")
	}
}

func TestMismatchedLengthsIsAnError(t *testing.T) {
	if err := ledger.WriteAll(&bytes.Buffer{}, []model.Transaction{{}}, nil); err == nil {
		t.Fatal("expected an error when entries do not line up with transactions")
	}
}

// Account directives render before the entries so a hand-kept file's letterhead survives a rewrite.
// The multi-line address stored under one key splits back into one `address` line each, in order,
// and the whole thing parses back to the metadata it came from: reader and writer are symmetric, so
// the artifact round-trips.
func TestWriteAccountsRoundTripsThroughTheReader(t *testing.T) {
	meta := map[string]map[string]string{
		"Assets:Consulting:Chequing": {"address": "742104 NB Inc.\n90 King Street\nBN: 770593416"},
		"Liabilities:Consulting:HST": {"address": "HST"},
	}
	var buf bytes.Buffer
	if err := ledger.WriteAccounts(&buf, meta); err != nil {
		t.Fatalf("WriteAccounts: %v", err)
	}
	if !strings.Contains(buf.String(), "account Assets:Consulting:Chequing\n  address 742104 NB Inc.\n  address 90 King Street\n  address BN: 770593416\n") {
		t.Fatalf("directive not rendered as address lines:\n%s", buf.String())
	}
	got, err := source.ReadLedgerAccounts(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ReadLedgerAccounts: %v", err)
	}
	if !reflect.DeepEqual(got, meta) {
		t.Fatalf("round trip = %+v, want %+v", got, meta)
	}
}

// Nothing to describe writes nothing, so books with no account metadata gain no stray lines.
func TestWriteAccountsWritesNothingWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := ledger.WriteAccounts(&buf, nil); err != nil {
		t.Fatalf("WriteAccounts: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("wrote %q for empty metadata", buf.String())
	}
}
