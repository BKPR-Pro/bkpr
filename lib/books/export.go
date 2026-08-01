package books

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/BKPR-Pro/bkpr/lib/eventlog"
)

// ActionExported records that a transaction was written out to a connector (e.g. the rent app
// booked this deposit against a lease). It is keyed by the transaction's fingerprint and is
// once-only: a deposit is recorded to at most one lease, so re-running an export skips it rather
// than paying twice.
const ActionExported = "exported"

// exportedData is the payload of a transaction.exported event: which connector took it, the lease
// it was booked against, what the connector called the record, and the amount, so the export is
// auditable from the log alone.
type exportedData struct {
	Connector   string `json:"connector"`
	Lease       string `json:"lease"`
	RecordedID  string `json:"recorded_id,omitempty"`
	AmountCents int64  `json:"amount_cents"`
}

// RecordExport marks a transaction as exported to a connector. It is idempotent: a transaction
// already exported is left as it is, so a caller may call this without first checking Exported.
func RecordExport(log *eventlog.Log, actor, txID, connector, lease, recordedID string, amountCents int64) error {
	data, err := json.Marshal(exportedData{
		Connector: connector, Lease: lease, RecordedID: recordedID, AmountCents: amountCents,
	})
	if err != nil {
		return err
	}
	_, err = log.TrackOnce(eventlog.Event{
		Collection: CollectionTransaction, RecordID: txID, Action: ActionExported,
		Version: version, Actor: actor, Data: data,
	})
	if errors.Is(err, eventlog.ErrAlreadyTracked) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("books: recording export of %s: %w", txID, err)
	}
	return nil
}

// Exported folds the set of transaction ids already written to a connector, so an export skips them.
func Exported(log *eventlog.Log) (map[string]bool, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, e := range events {
		if e.Collection == CollectionTransaction && e.Action == ActionExported {
			out[e.RecordID] = true
		}
	}
	return out, nil
}
