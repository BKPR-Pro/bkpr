package rentapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrAlreadyRecorded means the app already has this period recorded as paid -- almost always
// because a human recorded it from the app's own UI first. It is not a failure: the caller should
// treat it as the other valid path having gotten there first, not as something to retry.
var ErrAlreadyRecorded = errors.New("rentapp: already recorded")

// Payment is a cleared rent payment to record against a lease. Method, PaidOn, and Reference are
// optional; an empty one is not sent, so the endpoint falls back to its own default. IdempotencyKey
// makes the export safe to repeat: the rent app records the payment at most once per key, so
// re-running the export never double-records.
type Payment struct {
	LeaseID        string
	AmountCents    int64
	Method         string
	PaidOn         string // ISO date
	IdempotencyKey string
	Reference      string // e.g. an invoice number, appended to the app's rent description
}

// Recorded is what the rent app booked. Created distinguishes a fresh record (201) from a replay
// of a prior Idempotency-Key (200); both are success.
type Recorded struct {
	ID          string `json:"id"`
	LeaseID     string `json:"lease_id"`
	AmountCents int64  `json:"amount_cents"`
	Created     bool   `json:"-"`
}

// RecordRent posts a cleared rent payment to the lease's rent-roll endpoint.
func (c *Client) RecordRent(p Payment) (Recorded, error) {
	form := url.Values{}
	form.Set("amount", dollars(p.AmountCents))
	if p.Method != "" {
		form.Set("method", p.Method)
	}
	if p.PaidOn != "" {
		form.Set("paid_on", p.PaidOn)
	}
	if p.Reference != "" {
		form.Set("reference", p.Reference)
	}

	u := fmt.Sprintf("%s/rentroll/record/%s.json", c.baseURL, url.PathEscape(p.LeaseID))
	req, err := http.NewRequest(http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return Recorded{}, err
	}
	c.authorize(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if p.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", p.IdempotencyKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Recorded{}, fmt.Errorf("rentapp: recording rent for %s: %w", p.LeaseID, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Recorded{}, err
	}
	// 201 created, 200 replayed: both success. 409 is the app refusing a second recording of an
	// already-paid period -- wrap ErrAlreadyRecorded so the caller can tell it apart from a real
	// failure. Anything else is a real failure.
	if resp.StatusCode == http.StatusConflict {
		return Recorded{}, fmt.Errorf("rentapp: recording rent for %s: %s: %w", p.LeaseID, strings.TrimSpace(string(body)), ErrAlreadyRecorded)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return Recorded{}, fmt.Errorf("rentapp: recording rent returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var wrap struct {
		Transaction Recorded `json:"transaction"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return Recorded{}, fmt.Errorf("rentapp: reading the recorded payment: %w", err)
	}
	wrap.Transaction.Created = resp.StatusCode == http.StatusCreated
	return wrap.Transaction, nil
}

// dollars renders integer cents the way the endpoint reads an amount: a plain decimal string.
func dollars(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}
