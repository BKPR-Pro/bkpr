package books

import (
	"github.com/dallasread/bkpr/lib/eventlog"
)

// Doors folds the log into where each line of the books entered: an imported line maps to the actor
// that imported it (a connector or a statement file), and an accrual's synthetic line to the kind of
// accrual it is (invoice, bill). The register prints the door beside each line, and the twin
// detector groups collisions across doors, because one purchase entering through two doors is what
// fingerprints cannot dedupe. A fact is recorded once per line, so the map needs no tie-breaking.
func Doors(log *eventlog.Log) (map[string]string, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}
	doors := make(map[string]string)
	for _, e := range events {
		switch {
		case e.Collection == CollectionTransaction && e.Action == ActionImported:
			doors[e.RecordID] = e.Actor
		case e.Collection == CollectionInvoice && e.Action == ActionRaised,
			e.Collection == CollectionBill && e.Action == ActionReceived:
			doors[foldID(e.Collection, e.RecordID)] = e.Collection
		}
	}
	return doors, nil
}
