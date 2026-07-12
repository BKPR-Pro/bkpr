package books

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dallasread/bookkeeper/lib/eventlog"
	"github.com/dallasread/bookkeeper/lib/model"
)

const (
	// CollectionBill is keyed by a bill's fingerprint. A bill is money you owe, recognized before its
	// cash leaves: the mirror of an invoice. Like an invoice it is a fact a bank statement cannot
	// supply, because the money has not moved yet.
	CollectionBill = "bill"

	// ActionReceived records that a vendor's bill arrived: an expense incurred before any cash left.
	// It can only be true once per fingerprint, so recording the same bill twice is a no-op.
	ActionReceived = "received"

	// defaultPayable is where a bill parks until it is paid, when the caller names no account.
	defaultPayable = "Liabilities:Payable"
)

// Bill is money you owe, recognized before its cash: an expense you have incurred and been billed
// for but not yet paid. It is the mirror of an invoice, and shares its machinery; only the signs and
// the words differ. Its ID is a fingerprint of its content, the idempotency root.
type Bill struct {
	ID       string
	Date     time.Time    // when the expense was incurred, not when it will be paid
	Party    string       // the vendor billing you
	Amount   model.Amount // the magnitude owed; a positive quantity
	Category string       // the Expenses account the expense is recognized in
	Account  string       // the Liabilities:Payable account it parks in until paid
}

// ReceiveBill records a bill: an expense incurred and billed before the cash leaves. It returns the
// stored bill with its fingerprint, and whether the fact was new; recording the same one twice is a
// no-op, the way re-importing a statement is.
func ReceiveBill(log *eventlog.Log, actor, why string, bill Bill) (Bill, bool, error) {
	switch {
	case bill.Party == "":
		return Bill{}, false, fmt.Errorf("books: a bill needs a party")
	case bill.Amount.Commodity == "":
		return Bill{}, false, fmt.Errorf("books: a bill needs an amount")
	case bill.Amount.Units <= 0:
		return Bill{}, false, fmt.Errorf("books: a bill amount is a positive magnitude, got %s", bill.Amount)
	case bill.Category == "":
		return Bill{}, false, fmt.Errorf("books: a bill needs a category to recognize the expense in")
	case bill.Date.IsZero():
		return Bill{}, false, fmt.Errorf("books: a bill needs a date")
	}
	if bill.Account == "" {
		bill.Account = defaultPayable
	}
	bill.ID = accrualFingerprint(CollectionBill, bill.Date, bill.Party, bill.Amount, bill.Category, bill.Account)

	data, err := json.Marshal(accrualData{
		Date: bill.Date, Party: bill.Party, Amount: bill.Amount,
		Category: bill.Category, Account: bill.Account, Why: why,
	})
	if err != nil {
		return Bill{}, false, err
	}
	_, err = log.TrackOnce(eventlog.Event{
		Collection: CollectionBill, RecordID: bill.ID, Action: ActionReceived,
		Version: version, Actor: actor, Data: data,
	})
	switch {
	case errors.Is(err, eventlog.ErrAlreadyTracked):
		return bill, false, nil
	case err != nil:
		return Bill{}, false, fmt.Errorf("books: receiving %s: %w", bill.ID, err)
	default:
		return bill, true, nil
	}
}

// SettleBill records that a bank line paid a bill, so the cash clears the payable rather than booking
// the expense a second time (that was booked when the bill was received). An empty txID reopens the
// bill. A later settle supersedes.
func SettleBill(log *eventlog.Log, actor, billID, txID string) error {
	b, err := bill(log, billID)
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
	return trackSettlement(log, actor, CollectionBill, b.ID, txID)
}

// VoidBill supersedes a bill that should not have been received, the same operation as voiding a bad
// import. The received fact stays in the log; a later fact supersedes it.
func VoidBill(log *eventlog.Log, actor, why, billID string) error {
	b, err := bill(log, billID)
	if err != nil {
		return err
	}
	return trackVoid(log, actor, CollectionBill, why, b.ID)
}

// Bills folds the log into the bills it currently holds, in the order they were received, with the
// voided ones dropped.
func Bills(log *eventlog.Log) ([]Bill, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}

	voided := map[string]bool{}
	for _, e := range events {
		if e.Collection == CollectionBill && e.Action == ActionVoided {
			voided[e.RecordID] = true
		}
	}

	var out []Bill
	for _, e := range events {
		if e.Collection != CollectionBill || e.Action != ActionReceived {
			continue
		}
		if voided[e.RecordID] {
			continue
		}
		var data accrualData
		if err := e.Decode(&data); err != nil {
			return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
		}
		out = append(out, Bill{
			ID: e.RecordID, Date: data.Date, Party: data.Party,
			Amount: data.Amount, Category: data.Category, Account: data.Account,
		})
	}
	return out, nil
}

// bill folds out the one bill whose id is this fingerprint or uniquely begins with it, so a command
// can refuse to settle or void a fingerprint that was never received (or has since been voided)
// rather than record against nothing.
func bill(log *eventlog.Log, id string) (Bill, error) {
	bills, err := Bills(log)
	if err != nil {
		return Bill{}, err
	}
	b, ok, err := byPrefix(bills, id, func(b Bill) string { return b.ID })
	if err != nil {
		return Bill{}, err
	}
	if !ok {
		return Bill{}, fmt.Errorf("books: no bill %q; receive it before settling or voiding it", id)
	}
	return b, nil
}

// BillSettlements folds the current bank line, if any, that settles each bill.
func BillSettlements(log *eventlog.Log) (map[string]string, error) {
	return settlementsFor(log, CollectionBill)
}

// billLines folds the bills into the shared accrual shape: a payable is a liability, so the parked
// amount is the negative magnitude and the expense posting takes its negation (a positive debit).
func billLines(log *eventlog.Log) ([]accrualLine, error) {
	bills, err := Bills(log)
	if err != nil {
		return nil, err
	}
	settled, err := BillSettlements(log)
	if err != nil {
		return nil, err
	}
	out := make([]accrualLine, 0, len(bills))
	for _, b := range bills {
		out = append(out, accrualLine{
			kind: CollectionBill, id: b.ID, date: b.Date, party: b.Party,
			parkedAccount: b.Account, parkedAmount: b.Amount.Negate(), category: b.Category,
			settledBy: settled[b.ID],
		})
	}
	return out, nil
}
