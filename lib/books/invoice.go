package books

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dallasread/bkpr/lib/eventlog"
	"github.com/dallasread/bkpr/lib/model"
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
	Number   string       // the invoice number, rendered as the ledger (code); metadata, not part of the fingerprint

	// Sales tax collected on the customer's behalf, in the same words a rule uses. Amount is the
	// tax-inclusive gross owed, so the rate divides it: the net is recognized in Category and the tax
	// is parked in TaxAccount until it is remitted.
	TaxRate    string // e.g. "15%"
	TaxAccount string // the Liabilities account the tax is owed from
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
	if _, _, err := accrualTax("an invoice's", inv.TaxRate, inv.TaxAccount); err != nil {
		return Invoice{}, false, err
	}
	if inv.Account == "" {
		inv.Account = defaultReceivable
	}
	inv.ID = accrualFingerprint(CollectionInvoice, inv.Date, inv.Party, inv.Amount, inv.Category, inv.Account, inv.TaxRate, inv.TaxAccount)

	data, err := json.Marshal(accrualData{
		Date: inv.Date, Party: inv.Party, Amount: inv.Amount,
		Category: inv.Category, Account: inv.Account, Number: inv.Number, Why: why,
		TaxRate: inv.TaxRate, TaxAccount: inv.TaxAccount,
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

// SettleInvoice records that a bank line paid an invoice, so the cash clears the receivable rather
// than booking the income a second time (that was booked when the invoice was raised). An empty txID
// reopens the invoice. A later settle supersedes.
func SettleInvoice(log *eventlog.Log, actor, invoiceID, txID string) error {
	inv, err := invoice(log, invoiceID)
	if err != nil {
		return err
	}
	if txID != "" {
		tx, err := Transaction(log, txID)
		if err != nil {
			return err
		}
		txID = tx.ID // the caller may have quoted a prefix; the settlement keys to the line
	}
	return trackSettlement(log, actor, CollectionInvoice, inv.ID, txID)
}

// VoidInvoice supersedes an invoice that should not have been raised, the same operation as voiding
// a bad import. It does not delete the raised event; it appends a fact the fold honours, so the
// mistake and its correction both stay.
func VoidInvoice(log *eventlog.Log, actor, why, invoiceID string) error {
	inv, err := invoice(log, invoiceID)
	if err != nil {
		return err
	}
	return trackVoid(log, actor, CollectionInvoice, why, inv.ID)
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
		var data accrualData
		if err := e.Decode(&data); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}
		out = append(out, Invoice{
			ID: e.RecordID, Date: data.Date, Party: data.Party,
			Amount: data.Amount, Category: data.Category, Account: data.Account, Number: data.Number,
			TaxRate: data.TaxRate, TaxAccount: data.TaxAccount,
		})
	}
	return out, nil
}

// invoice folds out the one invoice whose id is this fingerprint or uniquely begins with it, so a
// command can refuse to settle or void a fingerprint that was never raised (or has since been
// voided) rather than record against nothing.
func invoice(log *eventlog.Log, id string) (Invoice, error) {
	invs, err := Invoices(log)
	if err != nil {
		return Invoice{}, err
	}
	inv, ok, err := byPrefix(invs, id, func(inv Invoice) string { return inv.ID })
	if err != nil {
		return Invoice{}, err
	}
	if !ok {
		return Invoice{}, fmt.Errorf("books: no invoice %q; raise it before settling or voiding it", id)
	}
	return inv, nil
}

// InvoiceSettlements folds the current bank line, if any, that settles each invoice.
func InvoiceSettlements(log *eventlog.Log) (map[string]string, error) {
	return settlementsFor(log, CollectionInvoice)
}

// invoiceLines folds the invoices into the shared accrual shape: a receivable is an asset, so the
// parked amount is the positive magnitude and the income posting takes its negation.
func invoiceLines(log *eventlog.Log) ([]accrualLine, error) {
	invs, err := Invoices(log)
	if err != nil {
		return nil, err
	}
	settled, err := InvoiceSettlements(log)
	if err != nil {
		return nil, err
	}
	out := make([]accrualLine, 0, len(invs))
	for _, inv := range invs {
		// Raise refused an unreadable rate, so a recorded one parses; a log hand-edited past that is
		// read as untaxed rather than failing the whole fold.
		num, den, _ := accrualTax("an invoice's", inv.TaxRate, inv.TaxAccount)
		out = append(out, accrualLine{
			kind: CollectionInvoice, id: inv.ID, date: inv.Date, party: inv.Party,
			parkedAccount: inv.Account, parkedAmount: inv.Amount, category: inv.Category,
			invoice: inv.Number, settledBy: settled[inv.ID],
			taxAccount: inv.TaxAccount, taxNum: num, taxDen: den,
		})
	}
	return out, nil
}
