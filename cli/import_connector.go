package main

import (
	"fmt"
	"path/filepath"

	"github.com/dallasread/bookkeeper/lib/adapters/source"
	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/model"
)

// fetchResult is what a connector fetch yields: the lines to import, and -- for a bank that showed it
// -- the account's current balance, recorded as a reconciliation anchor after the lines land.
type fetchResult struct {
	txs        []model.Transaction
	balance    model.Amount
	hasBalance bool
}

// connectorFetch pulls normalized transactions from one registered connector. The bytes come from
// the outside world (a bank's site), so a fetch lives in the driving adapter and the core never sees
// it, exactly as export does. Bound to a connector it becomes a Source, the input port import runs.
type connectorFetch func(books.Connector) (fetchResult, error)

// fetchOpts carries what a bank fetch needs beyond the connector itself: where its browser session is
// kept, whether a person is present to sign in again, and whether to force a fresh sign-in.
type fetchOpts struct {
	sessionDir  string
	snapshotDir string
	interactive bool
	relogin     bool
	history     int                // days of history to read this run; 0 means use the connector's default
	progress    func(stage string) // reports the browser's current stage for a live status; may be nil
}

// historyDays picks the history window: an import-time -history flag overrides the connector's stored
// default, so a one-time backfill needs no re-registering.
func historyDays(c books.Connector, o fetchOpts) int {
	if o.history > 0 {
		return o.history
	}
	return c.HistoryDays
}

// fetcherFor maps a connector kind to how its transactions are read. A bank (rbc, simplii,
// pcfinancial) is imported through its browser script; rentapp is export-only, so importing from it
// is refused rather than half-attempted. A kind with no reader is a clear error, so `import <name>`
// never silently records nothing.
//
// A bank's session file is keyed by the connector's token-env, not its name, so connectors that share
// a login (every RBC account) share one session: signing in through any of them signs in all.
func fetcherFor(kind string, o fetchOpts) (connectorFetch, error) {
	switch {
	case source.SupportsBank(kind):
		return func(c books.Connector) (fetchResult, error) {
			res, err := source.ReadBank(source.Bank{
				Institution:     c.Kind,
				Account:         c.Account,
				DefaultCurrency: c.Currency,
				LoginURL:        c.URL,
				SessionFile:     filepath.Join(o.sessionDir, c.TokenEnv+".json"),
				Interactive:     o.interactive,
				Relogin:         o.relogin,
				CredentialRefs:  c.Credentials,
				SecretCmd:       c.SecretCmd,
				SnapshotDir:     o.snapshotDir,
				AccountPath:     c.AccountPath,
				HistoryDays:     historyDays(c, o),
				Progress:        o.progress,
			})
			if err != nil {
				return fetchResult{}, err
			}
			return fetchResult{txs: res.Transactions, balance: res.Balance, hasBalance: res.HasBalance}, nil
		}, nil
	case kind == "rentapp":
		return nil, fmt.Errorf("import: connector kind %q is export-only; it cannot be imported from", kind)
	default:
		return nil, fmt.Errorf("import: no importer for connector kind %q yet", kind)
	}
}
