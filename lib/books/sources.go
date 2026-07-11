package books

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/dallasread/bookkeeper/lib/eventlog"
)

// CollectionSource is keyed by a source's name. A source is a live connector bookkeeper pulls from
// repeatedly, registered once and then imported by name, unlike a file which is a one-time input
// supplied inline.
const CollectionSource = "source"

// Source is how to reach one live connector. The token itself is never stored, because the log is
// committed to git: TokenEnv names the environment variable that holds it, read at import time.
type Source struct {
	Name     string `json:"-"`
	Kind     string `json:"kind"`      // which connector, e.g. rentapp
	URL      string `json:"url"`       // its base URL
	TokenEnv string `json:"token_env"` // env var holding the bearer token; never the token
	Account  string `json:"account"`   // ledger account its transactions land in
	Currency string `json:"currency"`  // their currency
}

// AddSource registers a source, or updates one under the same name. The token is deliberately not
// among its fields: only the name of the environment variable that carries it is stored.
func AddSource(log *eventlog.Log, actor string, s Source) error {
	switch {
	case s.Name == "":
		return fmt.Errorf("books: a source needs a name")
	case s.Kind == "":
		return fmt.Errorf("books: source %s needs a kind", s.Name)
	case s.URL == "":
		return fmt.Errorf("books: source %s needs a url", s.Name)
	case s.TokenEnv == "":
		return fmt.Errorf("books: source %s needs a token-env (the env var holding its token)", s.Name)
	case s.Account == "":
		return fmt.Errorf("books: source %s needs an account for its transactions", s.Name)
	case s.Currency == "":
		return fmt.Errorf("books: source %s needs a currency", s.Name)
	}

	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = log.Track(eventlog.Event{
		Collection: CollectionSource, RecordID: s.Name, Action: ActionAdded,
		Version: version, Actor: actor, Data: data,
	})
	return err
}

// RemoveSource forgets a source.
func RemoveSource(log *eventlog.Log, actor, name string) error {
	if _, ok, err := SourceByName(log, name); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("books: no source named %q", name)
	}
	_, err := log.Track(eventlog.Event{
		Collection: CollectionSource, RecordID: name, Action: ActionRemoved,
		Version: version, Actor: actor, Data: json.RawMessage("{}"),
	})
	return err
}

// Sources folds the log into the registered sources, ordered by name.
func Sources(log *eventlog.Log) ([]Source, error) {
	events, err := log.All()
	if err != nil {
		return nil, err
	}

	set := map[string]Source{}
	for _, e := range events {
		if e.Collection != CollectionSource {
			continue
		}
		switch e.Action {
		case ActionAdded:
			var s Source
			if err := e.Decode(&s); err != nil {
				return nil, fmt.Errorf("books: event %s: %w", e.ID, err)
			}
			s.Name = e.RecordID
			set[e.RecordID] = s
		case ActionRemoved:
			delete(set, e.RecordID)
		}
	}

	out := make([]Source, 0, len(set))
	for _, s := range set {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SourceByName finds one source. The bool distinguishes "no such source" from an error, so the
// import command can tell a source name from a file path.
func SourceByName(log *eventlog.Log, name string) (Source, bool, error) {
	set, err := Sources(log)
	if err != nil {
		return Source{}, false, err
	}
	for _, s := range set {
		if s.Name == name {
			return s, true, nil
		}
	}
	return Source{}, false, nil
}
