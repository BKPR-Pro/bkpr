package books_test

import (
	"strings"
	"testing"

	"github.com/dallasread/bkpr/lib/books"
)

func rentConnector() books.Connector {
	return books.Connector{
		Name: "rent", Kind: "rentapp", URL: "https://rent.example.ca",
		TokenEnv: "BK_RENT_TOKEN", Account: "Assets:Bank:Chequing", Currency: "CAD",
	}
}

func TestRegisterAndFindAConnector(t *testing.T) {
	log := newLog()
	if err := books.RegisterConnector(log, "human", rentConnector()); err != nil {
		t.Fatalf("RegisterConnector: %v", err)
	}

	got, ok, err := books.ConnectorByName(log, "rent")
	if err != nil || !ok {
		t.Fatalf("ConnectorByName: ok=%v err=%v", ok, err)
	}
	if got.Kind != "rentapp" || got.Account != "Assets:Bank:Chequing" || got.TokenEnv != "BK_RENT_TOKEN" {
		t.Errorf("got %+v", got)
	}
}

// The token itself must never reach the log, which is committed to git; only the name of the
// variable that holds it does.
func TestAConnectorStoresTheTokenEnvNotTheToken(t *testing.T) {
	log := newLog()
	books.RegisterConnector(log, "human", rentConnector())

	events, _ := log.All()
	for _, e := range events {
		if strings.Contains(string(e.Data), "secret") || strings.Contains(string(e.Data), "Bearer") {
			t.Fatalf("a token appears to have leaked into the log: %s", e.Data)
		}
		if e.Collection == "connector" && !strings.Contains(string(e.Data), "BK_RENT_TOKEN") {
			t.Errorf("the token-env name should be stored: %s", e.Data)
		}
	}
}

// A bank connector may carry credential references and a resolver command, so an unattended sign-in
// knows where to fetch its secrets. These are references (op://...), not secrets, so they round-trip
// through the git-committed log like any other connector field.
func TestConnectorRoundTripsCredentialReferences(t *testing.T) {
	log := newLog()
	c := books.Connector{
		Name: "rbc", Kind: "rbc", URL: "https://www.rbcroyalbank.com/", TokenEnv: "BK_RBC_SESSION",
		Account: "Assets:Bank:RBC", Currency: "CAD",
		SecretCmd: "op read {}",
		Credentials: map[string]string{
			"username": "op://Private/RBC/username",
			"password": "op://Private/RBC/password",
		},
	}
	if err := books.RegisterConnector(log, "human", c); err != nil {
		t.Fatalf("RegisterConnector: %v", err)
	}

	got, ok, err := books.ConnectorByName(log, "rbc")
	if err != nil || !ok {
		t.Fatalf("ConnectorByName: ok=%v err=%v", ok, err)
	}
	if got.SecretCmd != "op read {}" {
		t.Errorf("secret-cmd = %q, want the stored one", got.SecretCmd)
	}
	if got.Credentials["password"] != "op://Private/RBC/password" {
		t.Errorf("credential reference lost: %v", got.Credentials)
	}
}

// The stored references name where secrets live; a secret value must never reach the log.
func TestConnectorStoresCredentialReferencesNotSecrets(t *testing.T) {
	log := newLog()
	c := books.Connector{
		Name: "rbc", Kind: "rbc", URL: "https://www.rbcroyalbank.com/", TokenEnv: "BK_RBC_SESSION",
		Account: "Assets:Bank:RBC", Currency: "CAD",
		Credentials: map[string]string{"password": "op://Private/RBC/password"},
	}
	books.RegisterConnector(log, "human", c)

	events, _ := log.All()
	for _, e := range events {
		if e.Collection != "connector" {
			continue
		}
		if !strings.Contains(string(e.Data), "op://Private/RBC/password") {
			t.Errorf("the credential reference should be stored: %s", e.Data)
		}
	}
}

// Register is an upsert under the name, so re-registering changes the connector.
func TestReRegisteringAConnectorUpdatesIt(t *testing.T) {
	log := newLog()
	books.RegisterConnector(log, "human", rentConnector())

	changed := rentConnector()
	changed.Account = "Assets:Bank:Savings"
	if err := books.RegisterConnector(log, "human", changed); err != nil {
		t.Fatalf("RegisterConnector: %v", err)
	}

	got, _, _ := books.ConnectorByName(log, "rent")
	if got.Account != "Assets:Bank:Savings" {
		t.Errorf("account = %q, want the updated one", got.Account)
	}
	if set, _ := books.Connectors(log); len(set) != 1 {
		t.Fatalf("got %d connectors, want 1", len(set))
	}
}

func TestRemoveAConnector(t *testing.T) {
	log := newLog()
	books.RegisterConnector(log, "human", rentConnector())

	if err := books.RemoveConnector(log, "human", "rent"); err != nil {
		t.Fatalf("RemoveConnector: %v", err)
	}
	if _, ok, _ := books.ConnectorByName(log, "rent"); ok {
		t.Error("the connector is still registered")
	}
}

func TestAnUnknownNameIsNotAnError(t *testing.T) {
	// A command uses this to tell a registered name from something else, so absence must not error.
	_, ok, err := books.ConnectorByName(newLog(), "march.csv")
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if ok {
		t.Error("a name that was never registered should report not-found, not an error")
	}
}

func TestAConnectorMissingAFieldIsRefused(t *testing.T) {
	log := newLog()
	for _, break_ := range []func(*books.Connector){
		func(c *books.Connector) { c.Kind = "" },
		func(c *books.Connector) { c.URL = "" },
		func(c *books.Connector) { c.TokenEnv = "" },
		func(c *books.Connector) { c.Account = "" },
		func(c *books.Connector) { c.Currency = "" },
	} {
		c := rentConnector()
		break_(&c)
		if err := books.RegisterConnector(log, "human", c); err == nil {
			t.Errorf("added an incomplete connector: %+v", c)
		}
	}
}
