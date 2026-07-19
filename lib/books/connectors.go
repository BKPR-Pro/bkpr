package books

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/dallasread/bkpr/lib/eventlog"
)

const (
	// CollectionConnector is keyed by a connector's name. A connector is a live system bkpr
	// reaches over the network, registered once and then used by name, unlike a file which is a
	// one-time input supplied inline. It is bidirectional in principle: export writes to it today.
	CollectionConnector = "connector"

	// ActionRegistered records that a connector was registered under its name. "register" is the word
	// the domain uses for standing up a live connection, distinct from adding a row to a set: a
	// connector is a named endpoint you register, not an entry in an ordered list like a rule.
	ActionRegistered = "registered"
)

// Connector is how to reach one live system. The token itself is never stored, because the log is
// committed to git: TokenEnv names the environment variable that holds it, read when it is used.
type Connector struct {
	Name     string `json:"-"`
	Kind     string `json:"kind"`      // which system: rentapp (export), or a bank to import from: rbc, simplii, pcfinancial
	URL      string `json:"url"`       // its base or login URL
	TokenEnv string `json:"token_env"` // env var holding its secret; never the secret itself
	Account  string `json:"account"`   // ledger account its transactions land in
	Currency string `json:"currency"`  // default commodity for a line that carries none of its own

	// Credentials names, per field (username, password, security answers), where that secret lives --
	// a reference like "op://Private/RBC/password", never the secret. SecretCmd is the command that
	// turns a reference into its value ("op read {}" by default). Both are safe to commit: they say
	// where the secret is and how to fetch it, not what it is. Present only for a bank signing in
	// unattended; absent, sign-in is interactive.
	Credentials map[string]string `json:"credentials,omitempty"`
	SecretCmd   string            `json:"secret_cmd,omitempty"`

	// AccountPath is the ordered list of link/button labels to click after signing in to reach this
	// account, for a bank whose accounts have no stable URL (RBC). Several connectors share one login
	// and differ only by this path. Empty means the URL itself is the account.
	AccountPath []string `json:"account_path,omitempty"`

	// HistoryDays, when > 0, reads that many days back instead of the site's short default (RBC's
	// presets stop at 30 days), by driving its custom date-range filter. Imports dedupe by
	// fingerprint, so a wider window backfills without duplicating.
	HistoryDays int `json:"history_days,omitempty"`
}

// RegisterConnector registers a connector, or updates one under the same name. The token is
// deliberately not among its fields: only the name of the environment variable that carries it is
// stored.
func RegisterConnector(log *eventlog.Log, actor string, c Connector) error {
	switch {
	case c.Name == "":
		return fmt.Errorf("books: a connector needs a name")
	case c.Kind == "":
		return fmt.Errorf("books: connector %s needs a kind", c.Name)
	case c.URL == "":
		return fmt.Errorf("books: connector %s needs a url", c.Name)
	case c.TokenEnv == "":
		return fmt.Errorf("books: connector %s needs a token-env (the env var holding its token)", c.Name)
	case c.Account == "":
		return fmt.Errorf("books: connector %s needs an account for its transactions", c.Name)
	case c.Currency == "":
		return fmt.Errorf("books: connector %s needs a currency", c.Name)
	}

	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionConnector, RecordID: c.Name, Action: ActionRegistered,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// RemoveConnector forgets a connector.
func RemoveConnector(log *eventlog.Log, actor, name string) error {
	if _, ok, err := ConnectorByName(log, name); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("books: no connector named %q", name)
	}
	_, err := log.Track(eventlog.Event{
		Collection: CollectionConnector, RecordID: name, Action: ActionRemoved,
		Version: version, Actor: actor, Data: json.RawMessage("{}"),
	})
	return err
}

// Connectors folds the log into the registered connectors, ordered by name.
func Connectors(log *eventlog.Log) ([]Connector, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}

	set := map[string]Connector{}
	for _, e := range events {
		if e.Collection != CollectionConnector {
			continue
		}
		switch e.Action {
		case ActionRegistered:
			var c Connector
			if err := e.Decode(&c); err != nil {
				return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
			}
			c.Name = e.RecordID
			set[e.RecordID] = c
		case ActionRemoved:
			delete(set, e.RecordID)
		}
	}

	out := make([]Connector, 0, len(set))
	for _, c := range set {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ConnectorByName finds one connector. The bool distinguishes "no such connector" from an error, so
// a command can tell a registered name from something else.
func ConnectorByName(log *eventlog.Log, name string) (Connector, bool, error) {
	set, err := Connectors(log)
	if err != nil {
		return Connector{}, false, err
	}
	for _, c := range set {
		if c.Name == name {
			return c, true, nil
		}
	}
	return Connector{}, false, nil
}
