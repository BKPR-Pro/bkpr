package rentapp_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dallasread/bookkeeper/lib/adapters/rentapp"
)

func TestTransactionsPullsRecordedPayments(t *testing.T) {
	var gotPath, gotAuth, gotScope string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotScope = r.URL.Query().Get("scope")
		w.Write([]byte(`{
			"scope": "paid",
			"transactions": [
				{"id":"TX1","lease_id":"L1","kind":"rent","amount_cents":168000,
				 "description":"Rent for March 2026","method":"e-transfer","paid_at":"2026-03-03",
				 "paid_through":"2026-03-31","archived?":false},
				{"id":"TX2","lease_id":"L2","kind":"deposit","amount_cents":95000,
				 "description":"Damage deposit","method":"cheque","paid_at":"2026-03-05","archived?":false}
			]
		}`))
	}))
	defer srv.Close()

	txs, err := rentapp.New(srv.URL, "tok").Transactions("paid")
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}

	if gotPath != "/transactions.json" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotScope != "paid" {
		t.Errorf("scope = %q", gotScope)
	}
	if len(txs) != 2 {
		t.Fatalf("got %d transactions, want 2", len(txs))
	}
	first := txs[0]
	if first.ID != "TX1" || first.LeaseID != "L1" || first.Kind != "rent" || first.AmountCents != 168000 {
		t.Errorf("first = %+v", first)
	}
	if first.PaidAt != "2026-03-03" || first.Description != "Rent for March 2026" {
		t.Errorf("first date/desc = %+v", first)
	}
}

func TestTransactionsSurfacesAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, err := rentapp.New(srv.URL, "bad").Transactions(""); err == nil {
		t.Fatal("a 401 should be an error")
	}
}
