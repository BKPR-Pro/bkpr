package books

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
)

const (
	// CollectionAccrual is keyed by an accrual's fingerprint. An accrual is value recognized before
	// the cash moves: an invoice raises a receivable, a bill raises a payable. It is the one kind of
	// fact the bank statement cannot supply, because the money has not moved yet.
	CollectionAccrual = "accrual"

	// ActionRecognized records that revenue was earned, or an expense incurred, before any cash
	// changed hands. It can only be true once per fingerprint, so raising the same invoice twice is
	// a no-op the way re-importing a statement is.
	ActionRecognized = "recognized"

	// ActionSettled links an accrual to the bank line that paid it. The memo of a deposit does not
	// reliably name which invoice it clears, so the pairing is recorded rather than guessed, unlike
	// an internal transfer where each sighting names the other's account. A later settle supersedes.
	ActionSettled = "settled"

	// ActionVoided supersedes an accrual that should not have been raised, the way discard supersedes
	// a bad import. The recognized fact stays in the log; the fold drops it.
	ActionVoided = "voided"

	// KindInvoice is money owed to you: it debits a receivable and credits income.
	KindInvoice = "invoice"
	// KindBill is money you owe: it debits an expense and credits a payable.
	KindBill = "bill"

	// receivable and payable are where an invoice and a bill park until the cash arrives, when the
	// caller names no account of their own.
	defaultReceivable = "Assets:Receivable"
	defaultPayable    = "Liabilities:Payable"
)

// Accrual is one obligation recognized before its cash: an invoice (money owed to you) or a bill
// (money owed by you). It is authored out of what you know, exactly as unrecomputable as a rule,
// so it lives in the log. Its ID is a fingerprint of its content, which is the idempotency root:
// recognizing the same invoice twice records nothing the second time.
type Accrual struct {
	ID       string
	Kind     string       // KindInvoice or KindBill
	Date     time.Time    // when the value was earned or incurred, not when it will be paid
	Party    string       // the customer billed, or the vendor billing you
	Amount   model.Amount // the magnitude recognized; always positive, the sign follows the kind
	Category string       // the Income (invoice) or Expenses (bill) account the value is recognized in
	Account  string       // the Assets:Receivable (invoice) or Liabilities:Payable (bill) it parks in
}

// Basis is the lens the books are read through. Cash records only money that moved; accrual also
// books the value that was earned or incurred before it. Both are folds over the one log: the basis
// is chosen at read time, never stored, so a book of statements can be read either way and switched
// between them without rewriting anything.
type Basis string

const (
	CashBasis    Basis = "cash"
	AccrualBasis Basis = "accrual"
)

// recognizedData is the payload of an accrual.recognized event.
type recognizedData struct {
	Kind     string       `json:"kind"`
	Date     time.Time    `json:"date"`
	Party    string       `json:"party"`
	Amount   model.Amount `json:"amount"`
	Category string       `json:"category"`
	Account  string       `json:"account"`
	Why      string       `json:"why,omitempty"`
}

type settledData struct {
	Tx string `json:"tx,omitempty"` // the bank line that paid it; empty reopens the accrual
}

type voidedData struct {
	Why string `json:"why,omitempty"`
}

// Recognize records an invoice or a bill: value earned or incurred before the cash moves. The
// magnitude is stored positive and the kind decides the signs, so the caller states what happened
// ("a 1600.00 CAD invoice to J. Smith") rather than which way each posting points. It returns the
// stored accrual with its fingerprint, and whether the fact was new; recognizing the same one twice
// is a no-op, the way re-importing a statement is.
func Recognize(log *eventlog.Log, actor, why string, a Accrual) (Accrual, bool, error) {
	switch {
	case a.Kind != KindInvoice && a.Kind != KindBill:
		return Accrual{}, false, fmt.Errorf("books: an accrual is a %q or a %q, not %q", KindInvoice, KindBill, a.Kind)
	case a.Party == "":
		return Accrual{}, false, fmt.Errorf("books: an accrual needs a party")
	case a.Amount.Commodity == "":
		return Accrual{}, false, fmt.Errorf("books: an accrual needs an amount")
	case a.Amount.Units <= 0:
		return Accrual{}, false, fmt.Errorf("books: an accrual amount is a positive magnitude, got %s", a.Amount)
	case a.Category == "":
		return Accrual{}, false, fmt.Errorf("books: an accrual needs a category to recognize the value in")
	case a.Date.IsZero():
		return Accrual{}, false, fmt.Errorf("books: an accrual needs a date")
	}
	if a.Account == "" {
		a.Account = defaultReceivable
		if a.Kind == KindBill {
			a.Account = defaultPayable
		}
	}
	a.ID = accrualID(a)

	data, err := json.Marshal(recognizedData{
		Kind: a.Kind, Date: a.Date, Party: a.Party, Amount: a.Amount,
		Category: a.Category, Account: a.Account, Why: why,
	})
	if err != nil {
		return Accrual{}, false, err
	}
	_, err = log.TrackOnce(eventlog.Event{
		Collection: CollectionAccrual, RecordID: a.ID, Action: ActionRecognized,
		Version: version, Actor: actor, Data: data,
	})
	switch {
	case errors.Is(err, eventlog.ErrAlreadyTracked):
		return a, false, nil
	case err != nil:
		return Accrual{}, false, fmt.Errorf("books: recognizing %s: %w", a.ID, err)
	default:
		return a, true, nil
	}
}

// Settle records that a bank line paid an accrual, so the cash clears the receivable or payable
// rather than booking the income or expense a second time (that was already booked when the accrual
// was recognized). An empty txID reopens the accrual. A later settle supersedes.
func Settle(log *eventlog.Log, actor, accrualID, txID string) error {
	acc, err := accrual(log, accrualID)
	if err != nil {
		return err
	}
	if txID != "" {
		if _, err := Transaction(log, txID); err != nil {
			return err
		}
	}
	data, err := json.Marshal(settledData{Tx: txID})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionAccrual, RecordID: acc.ID, Action: ActionSettled,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// Void supersedes an accrual that should not have been raised. Like discard, it does not delete the
// recognized event; it appends a fact the fold honours, so the mistake and its correction both stay.
func Void(log *eventlog.Log, actor, why, accrualID string) error {
	if _, err := accrual(log, accrualID); err != nil {
		return err
	}
	data, err := json.Marshal(voidedData{Why: why})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionAccrual, RecordID: accrualID, Action: ActionVoided,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// Accruals folds the log into the accruals it currently holds, in recognition order, with the voided
// ones dropped. Recognition order is stable and total, so the accrual books render the same twice.
func Accruals(log *eventlog.Log) ([]Accrual, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}

	voided := map[string]bool{}
	for _, e := range events {
		if e.Collection == CollectionAccrual && e.Action == ActionVoided {
			voided[e.RecordID] = true
		}
	}

	var out []Accrual
	for _, e := range events {
		if e.Collection != CollectionAccrual || e.Action != ActionRecognized {
			continue
		}
		if voided[e.RecordID] {
			continue
		}
		var data recognizedData
		if err := e.Decode(&data); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}
		out = append(out, Accrual{
			ID: e.RecordID, Kind: data.Kind, Date: data.Date, Party: data.Party,
			Amount: data.Amount, Category: data.Category, Account: data.Account,
		})
	}
	return out, nil
}

// accrual folds out the one accrual with this id, so a command can refuse to settle or void a
// fingerprint that was never recognized (or has since been voided) rather than record against nothing.
func accrual(log *eventlog.Log, id string) (Accrual, error) {
	accs, err := Accruals(log)
	if err != nil {
		return Accrual{}, err
	}
	for _, a := range accs {
		if a.ID == id {
			return a, nil
		}
	}
	return Accrual{}, fmt.Errorf("books: no accrual %q; recognize it before settling or voiding it", id)
}

// settlements folds the current bank line, if any, that settles each accrual. A later settle
// supersedes, and an empty one reopens, so the map holds only accruals still linked to a line.
func Settlements(log *eventlog.Log) (map[string]string, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range events {
		if e.Collection != CollectionAccrual || e.Action != ActionSettled {
			continue
		}
		var d settledData
		if err := e.Decode(&d); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}
		if d.Tx == "" {
			delete(out, e.RecordID)
			continue
		}
		out[e.RecordID] = d.Tx
	}
	return out, nil
}

// transaction and entry render an accrual as a synthetic balanced line, so the same ledger writer
// that renders a bank statement renders an accrual with no special case. The parked account is the
// elided one, exactly as a statement's own account is: an invoice debits the receivable and credits
// income, a bill debits the expense and credits the payable.
func (a Accrual) transaction() model.Transaction {
	amount := a.Amount
	if a.Kind == KindBill {
		amount = amount.Negate()
	}
	return model.Transaction{
		ID:          "accrual:" + a.ID,
		Account:     a.Account,
		Date:        a.Date,
		Amount:      amount,
		Description: a.Party,
	}
}

func (a Accrual) entry() model.Entry {
	posted := a.Amount
	if a.Kind == KindInvoice {
		posted = posted.Negate()
	}
	return model.Entry{Payee: a.Party, Postings: []model.Posting{{Account: a.Category, Amount: posted}}}
}

// accrualID fingerprints an accrual by its content, the way a statement line is fingerprinted, so
// recognizing the same one twice collapses to one fact.
func accrualID(a Accrual) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		a.Kind,
		a.Date.Format("2006-01-02"),
		strings.Join(strings.Fields(strings.ToLower(a.Party)), " "),
		a.Amount.String(),
		a.Category,
		a.Account,
	}, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

// overlayAccruals turns the cash-basis books into accrual-basis ones. It books each open accrual as
// its own line, and where a bank line settled one it redirects that line from income or expense to
// the parked account, so the cash clears the receivable or payable instead of double-booking the
// value the accrual already recognized. The combined lines are re-sorted so accruals interleave in
// date order.
func overlayAccruals(log *eventlog.Log, txs []model.Transaction, entries []model.Entry) ([]model.Transaction, []model.Entry, error) {
	accs, err := Accruals(log)
	if err != nil {
		return nil, nil, err
	}
	settled, err := Settlements(log)
	if err != nil {
		return nil, nil, err
	}

	// A bank line that settled an accrual clears the parked account rather than booking income or
	// expense again: the value was recognized when the accrual was, and this is only its cash.
	byTx := map[string]Accrual{}
	for _, a := range accs {
		if tx, ok := settled[a.ID]; ok {
			byTx[tx] = a
		}
	}
	pos := make(map[string]int, len(txs))
	for i, tx := range txs {
		pos[tx.ID] = i
	}
	for txID, a := range byTx {
		i, ok := pos[txID]
		if !ok {
			continue // the line was discarded or suppressed; nothing to clear against
		}
		entries[i] = model.Entry{
			Payee:    entries[i].Payee,
			Postings: []model.Posting{{Account: a.Account, Amount: txs[i].Amount.Negate()}},
		}
	}

	for _, a := range accs {
		txs = append(txs, a.transaction())
		entries = append(entries, a.entry())
	}

	sortByDate(txs, entries)
	return txs, entries, nil
}

// sortByDate orders the lines and their entries together, by date and then id, so the artifact is
// byte-stable across runs the way Transactions already keeps the cash-basis lines.
func sortByDate(txs []model.Transaction, entries []model.Entry) {
	idx := make([]int, len(txs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		i, j := idx[a], idx[b]
		if !txs[i].Date.Equal(txs[j].Date) {
			return txs[i].Date.Before(txs[j].Date)
		}
		return txs[i].ID < txs[j].ID
	})
	st := make([]model.Transaction, len(txs))
	se := make([]model.Entry, len(entries))
	for n, i := range idx {
		st[n], se[n] = txs[i], entries[i]
	}
	copy(txs, st)
	copy(entries, se)
}
