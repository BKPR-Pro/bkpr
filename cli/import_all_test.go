package main

import (
	"testing"

	"github.com/dallasread/bkpr/lib/books"
)

func conn(name, kind, tokenEnv string) books.Connector {
	return books.Connector{Name: name, Kind: kind, TokenEnv: tokenEnv}
}

func connNames(cs []books.Connector) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Name
	}
	return out
}

// import -all runs connectors that share a login back to back, so each sign-in covers its whole group.
// orderConnectorsByLogin clusters connectors by their token-env (the login key), keeping the order the
// groups and their members first appear -- a stable reordering, never a sort.
func TestOrderConnectorsByLoginClustersSharedLogins(t *testing.T) {
	in := []books.Connector{
		conn("rbc-chequing", "rbc", "BK_RBC_SESSION"),
		conn("simplii-chequing", "simplii", "BK_SIMPLII_SESSION"),
		conn("rbc-visa", "rbc", "BK_RBC_SESSION"),
		conn("pcf-mastercard", "pcfinancial", "BK_PCF_SESSION"),
		conn("simplii-loc", "simplii", "BK_SIMPLII_SESSION"),
		conn("rbc-loc", "rbc", "BK_RBC_SESSION"),
	}
	got := connNames(orderConnectorsByLogin(in))
	want := []string{"rbc-chequing", "rbc-visa", "rbc-loc", "simplii-chequing", "simplii-loc", "pcf-mastercard"}
	if len(got) != len(want) {
		t.Fatalf("got %d connectors, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// import -all imports only the connectors that can be imported; an export-only kind (rentapp) is set
// aside and named, not half-run.
func TestImportableConnectorsSkipsExportOnly(t *testing.T) {
	in := []books.Connector{
		conn("rbc-chequing", "rbc", "BK_RBC_SESSION"),
		conn("rent", "rentapp", "BK_RENT"),
		conn("pcf-mastercard", "pcfinancial", "BK_PCF_SESSION"),
	}
	importable, skipped := importableConnectors(in)
	if got := connNames(importable); len(got) != 2 || got[0] != "rbc-chequing" || got[1] != "pcf-mastercard" {
		t.Errorf("importable = %v, want [rbc-chequing pcf-mastercard]", got)
	}
	if got := connNames(skipped); len(got) != 1 || got[0] != "rent" {
		t.Errorf("skipped = %v, want [rent]", got)
	}
}

// One connector failing (an expired sign-in, a selector drift) does not abort the batch: the rest still
// run, and the failures are reported by name.
func TestImportEachConnectorContinuesOnError(t *testing.T) {
	ordered := []books.Connector{
		conn("a", "rbc", "S1"),
		conn("b", "rbc", "S1"),
		conn("c", "simplii", "S2"),
	}
	var ran []string
	run := func(c books.Connector, o fetchOpts) error {
		ran = append(ran, c.Name)
		if c.Name == "b" {
			return errFetch
		}
		return nil
	}
	failed := importEachConnector(ordered, fetchOpts{}, run)
	if len(ran) != 3 {
		t.Errorf("ran %v, want all three despite b failing", ran)
	}
	if len(failed) != 1 || failed[0] != "b" {
		t.Errorf("failed = %v, want [b]", failed)
	}
}

// With -relogin, a login is refreshed once, not once per account: only the first connector of each
// token-env signs in fresh; the rest reuse the session it just saved -- the whole point of running a
// shared login's accounts together.
func TestImportEachConnectorRelogsInOncePerLogin(t *testing.T) {
	ordered := []books.Connector{
		conn("rbc-1", "rbc", "S1"),
		conn("rbc-2", "rbc", "S1"),
		conn("simplii-1", "simplii", "S2"),
		conn("simplii-2", "simplii", "S2"),
	}
	relogin := map[string]bool{}
	run := func(c books.Connector, o fetchOpts) error {
		relogin[c.Name] = o.relogin
		return nil
	}
	importEachConnector(ordered, fetchOpts{relogin: true}, run)
	want := map[string]bool{"rbc-1": true, "rbc-2": false, "simplii-1": true, "simplii-2": false}
	for name, w := range want {
		if relogin[name] != w {
			t.Errorf("%s relogin = %v, want %v", name, relogin[name], w)
		}
	}
}
