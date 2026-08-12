package books

import (
	"fmt"
	"strconv"

	"github.com/BKPR-Pro/bkpr/lib/eventlog"
)

// NextInvoiceNumber folds the log for the number the next invoice should carry: one past the highest
// number the book has already issued, or "1" when it has issued none.
//
// A number is issued in two places -- on a raised invoice and on a categorized bank line, where a
// payment is tagged with the invoice it settles -- and both are the one sequence, so both are read.
// A received bill is not: its number is the vendor's own, not ours, and folding it in would push our
// sequence to wherever their numbering happens to be.
//
// Only entirely numeric numbers count, because a number that is not ours to issue (a vendor's
// "INV-7c") is not part of the sequence, and picking the digits out of one would read it as though
// it were.
//
// It is the highest plus one, never a gap. A missing number is not proof the number is free: it may
// already be printed on a document that was withheld or voided, and reissuing it would put two
// different invoices on one number. Monotonic costs nothing but a hole in the sequence.
func NextInvoiceNumber(log *eventlog.Log) (string, error) {
	events, err := log.All()
	if err != nil {
		return "", err
	}

	var max int64
	consider := func(number string) {
		n, err := strconv.ParseInt(number, 10, 64)
		// ParseInt takes a sign and leading whitespace, neither of which is a number we issued.
		if err != nil || number == "" || number[0] < '0' || number[0] > '9' {
			return
		}
		if n > max {
			max = n
		}
	}

	for _, e := range events {
		switch {
		case e.Collection == CollectionInvoice && e.Action == ActionRaised:
			// The raised event, not the folded invoice: a voided invoice's number is still spent,
			// because the document may have gone out before the void.
			var d accrualData
			if err := e.Decode(&d); err != nil {
				return "", fmt.Errorf("books: event %s: %w", e.ID, err)
			}
			consider(d.Number)
		case e.Collection == CollectionTransaction && e.Action == ActionCategorized:
			var d categorizedData
			if err := e.Decode(&d); err != nil {
				return "", fmt.Errorf("books: event %s: %w", e.ID, err)
			}
			consider(d.Invoice)
		}
	}
	return strconv.FormatInt(max+1, 10), nil
}
