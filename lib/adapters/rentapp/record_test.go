package rentapp_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"bkpr.pro/bkpr/lib/adapters/rentapp"
)

// A recorded rent payment is exported to the lease's rent-roll endpoint, carrying the real cleared
// amount and date and an Idempotency-Key, so the rent app knows the rent is paid.
func TestRecordRentPostsThePaymentWithAnIdempotencyKey(t *testing.T) {
	var gotPath, gotMethod, gotKey, gotAuth string
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		gotKey = r.Header.Get("Idempotency-Key")
		gotAuth = r.Header.Get("Authorization")
		r.ParseForm()
		gotForm = r.PostForm
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"transaction":{"id":"TX9","lease_id":"L1","amount_cents":168000,"kind":"rent"}}`))
	}))
	defer srv.Close()

	got, err := rentapp.New(srv.URL, "tok").RecordRent(rentapp.Payment{
		LeaseID: "L1", AmountCents: 168000, Method: "e-transfer", PaidOn: "2026-03-03",
		IdempotencyKey: "key-abc",
	})
	if err != nil {
		t.Fatalf("RecordRent: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != "/rentroll/record/L1.json" {
		t.Errorf("%s %s, want POST /rentroll/record/L1.json", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotKey != "key-abc" {
		t.Errorf("Idempotency-Key = %q, want key-abc", gotKey)
	}
	// The amount is sent as a dollar string, since that is what the endpoint reads.
	if gotForm.Get("amount") != "1680.00" {
		t.Errorf("amount = %q, want 1680.00", gotForm.Get("amount"))
	}
	if gotForm.Get("method") != "e-transfer" || gotForm.Get("paid_on") != "2026-03-03" {
		t.Errorf("form = %v", gotForm)
	}
	if got.ID != "TX9" || !got.Created {
		t.Errorf("got %+v, want the returned id and Created", got)
	}
}

// A replay returns 200 with the same transaction. The export must read that as already-done, not a
// failure, so re-running never double-records and never errors.
func TestRecordRentTreatsAReplayAsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // replay of a prior Idempotency-Key
		w.Write([]byte(`{"transaction":{"id":"TX9","lease_id":"L1"}}`))
	}))
	defer srv.Close()

	got, err := rentapp.New(srv.URL, "tok").RecordRent(rentapp.Payment{LeaseID: "L1", AmountCents: 168000, IdempotencyKey: "k"})
	if err != nil {
		t.Fatalf("RecordRent: %v", err)
	}
	if got.ID != "TX9" || got.Created {
		t.Errorf("got %+v, want the id and Created=false on a replay", got)
	}
}

func TestRecordRentSurfacesARejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "conflict", http.StatusConflict)
	}))
	defer srv.Close()

	if _, err := rentapp.New(srv.URL, "tok").RecordRent(rentapp.Payment{LeaseID: "L1", AmountCents: 1, IdempotencyKey: "k"}); err == nil {
		t.Fatal("a 409 should surface as an error")
	}
}

// Omitted optional fields are simply not sent, so the endpoint falls back to its own defaults
// rather than being handed empty strings.
func TestRecordRentOmitsEmptyOptionalFields(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		form = r.PostForm
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"transaction":{"id":"TX"}}`))
	}))
	defer srv.Close()

	_, err := rentapp.New(srv.URL, "tok").RecordRent(rentapp.Payment{LeaseID: "L1", AmountCents: 168000, IdempotencyKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, sent := form["method"]; sent {
		t.Errorf("empty method should be omitted, got %v", form)
	}
	if _, sent := form["paid_on"]; sent {
		t.Errorf("empty paid_on should be omitted, got %v", form)
	}
}
