package books

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dallasread/bookkeeper/lib/eventlog"
)

// ActionPushed records that a transaction was recorded to a destination (e.g. the rent app booked
// this deposit against a lease). It is keyed by the transaction's fingerprint and is once-only: a
// deposit is recorded to at most one lease, so re-running a push skips it rather than paying twice.
const ActionPushed = "pushed"

// pushedData is the payload of a transaction.pushed event: which destination took it, the lease it
// was booked against, what the destination called the record, and the amount, so the push is
// auditable from the log alone.
type pushedData struct {
	Destination string `json:"destination"`
	Lease       string `json:"lease"`
	RecordedID  string `json:"recorded_id,omitempty"`
	AmountCents int64  `json:"amount_cents"`
}

// RecordPush marks a transaction as pushed to a destination. It is idempotent: a transaction
// already pushed is left as it is, so a caller may call this without first checking Pushed.
func RecordPush(log *eventlog.Log, actor, txID, destination, lease, recordedID string, amountCents int64) error {
	data, err := json.Marshal(pushedData{
		Destination: destination, Lease: lease, RecordedID: recordedID, AmountCents: amountCents,
	})
	if err != nil {
		return err
	}
	_, err = log.TrackOnce(eventlog.Event{
		Collection: CollectionTransaction, RecordID: txID, Action: ActionPushed,
		Version: version, Actor: actor, Data: data,
	})
	if errors.Is(err, eventlog.ErrAlreadyTracked) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("books: recording push of %s: %w", txID, err)
	}
	return nil
}

// Pushed folds the set of transaction ids already recorded to a destination, so a push skips them.
func Pushed(log *eventlog.Log) (map[string]bool, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, e := range events {
		if e.Collection == CollectionTransaction && e.Action == ActionPushed {
			out[e.RecordID] = true
		}
	}
	return out, nil
}
