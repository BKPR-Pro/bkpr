package books

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/dallasread/bookkeeper/lib/eventlog"
)

// CollectionConnector is keyed by a connector's name. A connector is a live system bookkeeper
// reaches over the network, registered once and then used by name, unlike a file which is a
// one-time input supplied inline. It is bidirectional in principle: push writes to it today.
const CollectionConnector = "connector"

// Connector is how to reach one live system. The token itself is never stored, because the log is
// committed to git: TokenEnv names the environment variable that holds it, read when it is used.
type Connector struct {
	Name     string `json:"-"`
	Kind     string `json:"kind"`      // which connector, e.g. rentapp
	URL      string `json:"url"`       // its base URL
	TokenEnv string `json:"token_env"` // env var holding the bearer token; never the token
	Account  string `json:"account"`   // ledger account its transactions land in
	Currency string `json:"currency"`  // their currency
}

// AddConnector registers a connector, or updates one under the same name. The token is deliberately
// not among its fields: only the name of the environment variable that carries it is stored.
func AddConnector(log *eventlog.Log, actor string, c Connector) error {
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
		Collection: CollectionConnector, RecordID: c.Name, Action: ActionAdded,
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
		case ActionAdded:
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
