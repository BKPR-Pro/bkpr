package main

import (
	"fmt"
	"path/filepath"

	"github.com/dallasread/bookkeeper/lib/adapters/source"
	"github.com/dallasread/bookkeeper/lib/books"
	"github.com/dallasread/bookkeeper/lib/model"
)

// connectorFetch pulls normalized transactions from one registered connector. The bytes come from
// the outside world (a bank's site), so a fetch lives in the driving adapter and the core never sees
// it, exactly as export does. Bound to a connector it becomes a Source, the input port import runs.
type connectorFetch func(books.Connector) ([]model.Transaction, error)

// fetchOpts carries what a bank fetch needs beyond the connector itself: where its browser session is
// kept, whether a person is present to sign in again, and whether to force a fresh sign-in.
type fetchOpts struct {
	sessionDir  string
	interactive bool
	relogin     bool
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
		return func(c books.Connector) ([]model.Transaction, error) {
			return source.ReadBank(source.Bank{
				Institution:     c.Kind,
				Account:         c.Account,
				DefaultCurrency: c.Currency,
				LoginURL:        c.URL,
				SessionFile:     filepath.Join(o.sessionDir, c.TokenEnv+".json"),
				Interactive:     o.interactive,
				Relogin:         o.relogin,
			})
		}, nil
	case kind == "rentapp":
		return nil, fmt.Errorf("import: connector kind %q is export-only; it cannot be imported from", kind)
	default:
		return nil, fmt.Errorf("import: no importer for connector kind %q yet", kind)
	}
}
