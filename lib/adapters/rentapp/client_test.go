package rentapp_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dallasread/bookkeeper/lib/adapters/rentapp"
)

// The rent app is a spoke: bookkeeper pulls the rent roll to learn what is expected, and pushes
// recorded payments back. This exercises the pull side against a stand-in server, so no live call
// is made in a test.
func TestLeasesReadsTheRentRoll(t *testing.T) {
	var gotAuth, gotScope, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotScope = r.URL.Query().Get("scope")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"as_of": "2026-03-10",
			"entries": [
				{"lease_id":"L1","property_id":"P1","rent_cents":160000,"total_cents":168000,
				 "next_due_on":"2026-03-01","paid_through":"2026-02-28","overdue":true},
				{"lease_id":"L2","property_id":"P2","rent_cents":95000,"total_cents":95000,
				 "next_due_on":"2026-04-01","paid_through":"2026-03-31","overdue":false}
			]
		}`))
	}))
	defer srv.Close()

	leases, err := rentapp.New(srv.URL, "secret-token").Leases("active")
	if err != nil {
		t.Fatalf("Leases: %v", err)
	}

	if gotPath != "/leases.json" {
		t.Errorf("path = %q, want /leases.json", gotPath)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("auth = %q, want the bearer token", gotAuth)
	}
	if gotScope != "active" {
		t.Errorf("scope = %q, want active", gotScope)
	}
	if len(leases) != 2 {
		t.Fatalf("got %d leases, want 2", len(leases))
	}
	first := leases[0]
	if first.ID != "L1" || first.PropertyID != "P1" || first.TotalCents != 168000 {
		t.Errorf("first lease = %+v", first)
	}
	if first.NextDueOn != "2026-03-01" || !first.Overdue {
		t.Errorf("first lease dates/flags = %+v", first)
	}
}

func TestLeasesSurfacesAServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, err := rentapp.New(srv.URL, "bad").Leases("active"); err == nil {
		t.Fatal("a 401 should be an error")
	}
}

// The total including taxes is what the bank deposit will match, so it is the amount a payment is
// reconciled against.
func TestLeaseExposesTheTotalAsTheExpectedAmount(t *testing.T) {
	l := rentapp.Lease{RentCents: 160000, TotalCents: 168000}
	if l.ExpectedCents() != 168000 {
		t.Errorf("ExpectedCents = %d, want the tax-inclusive total", l.ExpectedCents())
	}
}
