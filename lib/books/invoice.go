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
	// CollectionInvoice is keyed by an invoice's fingerprint. An invoice is money owed to you,
	// recognized before its cash arrives: it is the one kind of fact a bank statement cannot supply,
	// because the money has not moved yet.
	CollectionInvoice = "invoice"

	// ActionRaised records that an invoice was issued: revenue earned before any cash changed hands.
	// It can only be true once per fingerprint, so raising the same invoice twice is a no-op the way
	// re-importing a statement is.
	ActionRaised = "raised"

	// ActionSettled links an invoice to the bank line that paid it. The memo of a deposit does not
	// reliably name which invoice it clears, so the pairing is recorded rather than guessed, unlike
	// an internal transfer where each sighting names the other's account. A later settle supersedes.
	ActionSettled = "settled"

	// ActionVoided supersedes an invoice that should not have been raised, the way discard supersedes
	// a bad import. The raised fact stays in the log; the fold drops it.
	ActionVoided = "voided"

	// defaultReceivable is where an invoice parks until its cash arrives, when the caller names no
	// account of their own.
	defaultReceivable = "Assets:Receivable"
)

// Invoice is money owed to you, recognized before its cash: revenue you have earned and billed but
// not yet been paid for. It is authored out of what you know, exactly as unrecomputable as a rule,
// so it lives in the log. Its ID is a fingerprint of its content, which is the idempotency root:
// raising the same invoice twice records nothing the second time.
type Invoice struct {
	ID       string
	Date     time.Time    // when the revenue was earned, not when it will be paid
	Party    string       // the customer billed
	Amount   model.Amount // the magnitude owed; a positive quantity
	Category string       // the Income account the revenue is recognized in
	Account  string       // the Assets:Receivable account it parks in until paid
}

// raisedData is the payload of an invoice.raised event.
type raisedData struct {
	Date     time.Time    `json:"date"`
	Party    string       `json:"party"`
	Amount   model.Amount `json:"amount"`
	Category string       `json:"category"`
	Account  string       `json:"account"`
	Why      string       `json:"why,omitempty"`
}

type settledData struct {
	Tx string `json:"tx,omitempty"` // the bank line that paid it; empty reopens the invoice
}

type voidedData struct {
	Why string `json:"why,omitempty"`
}

// Raise records an invoice: revenue earned and billed before the cash moves. It returns the stored
// invoice with its fingerprint, and whether the fact was new; raising the same one twice is a no-op,
// the way re-importing a statement is.
func Raise(log *eventlog.Log, actor, why string, inv Invoice) (Invoice, bool, error) {
	switch {
	case inv.Party == "":
		return Invoice{}, false, fmt.Errorf("books: an invoice needs a party")
	case inv.Amount.Commodity == "":
		return Invoice{}, false, fmt.Errorf("books: an invoice needs an amount")
	case inv.Amount.Units <= 0:
		return Invoice{}, false, fmt.Errorf("books: an invoice amount is a positive magnitude, got %s", inv.Amount)
	case inv.Category == "":
		return Invoice{}, false, fmt.Errorf("books: an invoice needs a category to recognize the revenue in")
	case inv.Date.IsZero():
		return Invoice{}, false, fmt.Errorf("books: an invoice needs a date")
	}
	if inv.Account == "" {
		inv.Account = defaultReceivable
	}
	inv.ID = invoiceID(inv)

	data, err := json.Marshal(raisedData{
		Date: inv.Date, Party: inv.Party, Amount: inv.Amount,
		Category: inv.Category, Account: inv.Account, Why: why,
	})
	if err != nil {
		return Invoice{}, false, err
	}
	_, err = log.TrackOnce(eventlog.Event{
		Collection: CollectionInvoice, RecordID: inv.ID, Action: ActionRaised,
		Version: version, Actor: actor, Data: data,
	})
	switch {
	case errors.Is(err, eventlog.ErrAlreadyTracked):
		return inv, false, nil
	case err != nil:
		return Invoice{}, false, fmt.Errorf("books: raising %s: %w", inv.ID, err)
	default:
		return inv, true, nil
	}
}

// Settle records that a bank line paid an invoice, so the cash clears the receivable rather than
// booking the income a second time (that was booked when the invoice was raised). An empty txID
// reopens the invoice. A later settle supersedes.
func Settle(log *eventlog.Log, actor, invoiceID, txID string) error {
	inv, err := invoice(log, invoiceID)
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
		Collection: CollectionInvoice, RecordID: inv.ID, Action: ActionSettled,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// Void supersedes an invoice that should not have been raised. Like discard, it does not delete the
// raised event; it appends a fact the fold honours, so the mistake and its correction both stay.
func Void(log *eventlog.Log, actor, why, invoiceID string) error {
	if _, err := invoice(log, invoiceID); err != nil {
		return err
	}
	data, err := json.Marshal(voidedData{Why: why})
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionInvoice, RecordID: invoiceID, Action: ActionVoided,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// Invoices folds the log into the invoices it currently holds, in the order they were raised, with
// the voided ones dropped. That order is stable and total, so the accrual-basis books render the
// same twice.
func Invoices(log *eventlog.Log) ([]Invoice, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}

	voided := map[string]bool{}
	for _, e := range events {
		if e.Collection == CollectionInvoice && e.Action == ActionVoided {
			voided[e.RecordID] = true
		}
	}

	var out []Invoice
	for _, e := range events {
		if e.Collection != CollectionInvoice || e.Action != ActionRaised {
			continue
		}
		if voided[e.RecordID] {
			continue
		}
		var data raisedData
		if err := e.Decode(&data); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}
		out = append(out, Invoice{
			ID: e.RecordID, Date: data.Date, Party: data.Party,
			Amount: data.Amount, Category: data.Category, Account: data.Account,
		})
	}
	return out, nil
}

// invoice folds out the one invoice with this id, so a command can refuse to settle or void a
// fingerprint that was never raised (or has since been voided) rather than record against nothing.
func invoice(log *eventlog.Log, id string) (Invoice, error) {
	invs, err := Invoices(log)
	if err != nil {
		return Invoice{}, err
	}
	for _, inv := range invs {
		if inv.ID == id {
			return inv, nil
		}
	}
	return Invoice{}, fmt.Errorf("books: no invoice %q; raise it before settling or voiding it", id)
}

// Settlements folds the current bank line, if any, that settles each invoice. A later settle
// supersedes, and an empty one reopens, so the map holds only invoices still linked to a line.
func Settlements(log *eventlog.Log) (map[string]string, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range events {
		if e.Collection != CollectionInvoice || e.Action != ActionSettled {
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

// transaction and entry render an invoice as a synthetic balanced line, so the same ledger writer
// that renders a bank statement renders an invoice with no special case. The receivable is the
// elided account, exactly as a statement's own account is: the invoice debits the receivable and
// credits income.
func (inv Invoice) transaction() model.Transaction {
	return model.Transaction{
		ID:          "invoice:" + inv.ID,
		Account:     inv.Account,
		Date:        inv.Date,
		Amount:      inv.Amount,
		Description: inv.Party,
	}
}

func (inv Invoice) entry() model.Entry {
	return model.Entry{Payee: inv.Party, Postings: []model.Posting{{Account: inv.Category, Amount: inv.Amount.Negate()}}}
}

// invoiceID fingerprints an invoice by its content, the way a statement line is fingerprinted, so
// raising the same one twice collapses to one fact.
func invoiceID(inv Invoice) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		inv.Date.Format("2006-01-02"),
		strings.Join(strings.Fields(strings.ToLower(inv.Party)), " "),
		inv.Amount.String(),
		inv.Category,
		inv.Account,
	}, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

// overlayInvoices turns the cash-basis books into accrual-basis ones. It books each open invoice as
// its own line, and where a bank line settled one it redirects that line from income to the
// receivable, so the cash clears the receivable instead of double-booking the revenue the invoice
// already recognized. The combined lines are re-sorted so invoices interleave in date order.
func overlayInvoices(log *eventlog.Log, txs []model.Transaction, entries []model.Entry) ([]model.Transaction, []model.Entry, error) {
	invs, err := Invoices(log)
	if err != nil {
		return nil, nil, err
	}
	settled, err := Settlements(log)
	if err != nil {
		return nil, nil, err
	}

	// A bank line that settled an invoice clears the receivable rather than booking income again: the
	// revenue was recognized when the invoice was, and this is only its cash.
	byTx := map[string]Invoice{}
	for _, inv := range invs {
		if tx, ok := settled[inv.ID]; ok {
			byTx[tx] = inv
		}
	}
	pos := make(map[string]int, len(txs))
	for i, tx := range txs {
		pos[tx.ID] = i
	}
	for txID, inv := range byTx {
		i, ok := pos[txID]
		if !ok {
			continue // the line was discarded or suppressed; nothing to clear against
		}
		entries[i] = model.Entry{
			Payee:    entries[i].Payee,
			Postings: []model.Posting{{Account: inv.Account, Amount: txs[i].Amount.Negate()}},
		}
	}

	for _, inv := range invs {
		txs = append(txs, inv.transaction())
		entries = append(entries, inv.entry())
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
