package books_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bookkeeper/lib/books"
)

func rentSource() books.Source {
	return books.Source{
		Name: "rent", Kind: "rentapp", URL: "https://rent.example.ca",
		TokenEnv: "BK_RENT_TOKEN", Account: "Assets:Bank:Chequing", Currency: "CAD",
	}
}

func TestAddAndFindASource(t *testing.T) {
	log := newLog()
	if err := books.AddSource(log, "human", rentSource()); err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	got, ok, err := books.SourceByName(log, "rent")
	if err != nil || !ok {
		t.Fatalf("SourceByName: ok=%v err=%v", ok, err)
	}
	if got.Kind != "rentapp" || got.Account != "Assets:Bank:Chequing" || got.TokenEnv != "BK_RENT_TOKEN" {
		t.Errorf("got %+v", got)
	}
}

// The token itself must never reach the log, which is committed to git; only the name of the
// variable that holds it does.
func TestASourceStoresTheTokenEnvNotTheToken(t *testing.T) {
	log := newLog()
	books.AddSource(log, "human", rentSource())

	events, _ := log.All()
	for _, e := range events {
		if strings.Contains(string(e.Data), "secret") || strings.Contains(string(e.Data), "Bearer") {
			t.Fatalf("a token appears to have leaked into the log: %s", e.Data)
		}
		if e.Collection == "source" && !strings.Contains(string(e.Data), "BK_RENT_TOKEN") {
			t.Errorf("the token-env name should be stored: %s", e.Data)
		}
	}
}

// Add is an upsert under the name, so re-registering changes the source.
func TestReAddingASourceUpdatesIt(t *testing.T) {
	log := newLog()
	books.AddSource(log, "human", rentSource())

	changed := rentSource()
	changed.Account = "Assets:Bank:Savings"
	if err := books.AddSource(log, "human", changed); err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	got, _, _ := books.SourceByName(log, "rent")
	if got.Account != "Assets:Bank:Savings" {
		t.Errorf("account = %q, want the updated one", got.Account)
	}
	if set, _ := books.Sources(log); len(set) != 1 {
		t.Fatalf("got %d sources, want 1", len(set))
	}
}

func TestRemoveASource(t *testing.T) {
	log := newLog()
	books.AddSource(log, "human", rentSource())

	if err := books.RemoveSource(log, "human", "rent"); err != nil {
		t.Fatalf("RemoveSource: %v", err)
	}
	if _, ok, _ := books.SourceByName(log, "rent"); ok {
		t.Error("the source is still registered")
	}
}

func TestAnUnknownNameIsNotAnError(t *testing.T) {
	// import uses this to tell a source name from a file path, so absence must not be an error.
	_, ok, err := books.SourceByName(newLog(), "march.csv")
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if ok {
		t.Error("a name that was never registered should report not-found, not an error")
	}
}

func TestASourceMissingAFieldIsRefused(t *testing.T) {
	log := newLog()
	for _, break_ := range []func(*books.Source){
		func(s *books.Source) { s.Kind = "" },
		func(s *books.Source) { s.URL = "" },
		func(s *books.Source) { s.TokenEnv = "" },
		func(s *books.Source) { s.Account = "" },
		func(s *books.Source) { s.Currency = "" },
	} {
		s := rentSource()
		break_(&s)
		if err := books.AddSource(log, "human", s); err == nil {
			t.Errorf("added an incomplete source: %+v", s)
		}
	}
}
