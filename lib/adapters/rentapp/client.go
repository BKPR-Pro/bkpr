// Package rentapp is the connector to the rent app, the one place that knows its endpoints.
//
// The rent app is a spoke, both a source and a destination. Bookkeeper pulls the rent roll to
// learn what rent is expected and for which lease, and pushes recorded rent payments back so the
// rent app's paid/unpaid state stays current. The core of bookkeeper never imports this package;
// only the CLI and a future push command do, which keeps the books standalone.
package rentapp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Lease is one row of the rent roll: a lease, what it owes, and when.
type Lease struct {
	ID          string `json:"lease_id"`
	PropertyID  string `json:"property_id"`
	RentCents   int64  `json:"rent_cents"`
	TotalCents  int64  `json:"total_cents"` // rent including taxes
	NextDueOn   string `json:"next_due_on"`
	PaidThrough string `json:"paid_through"`
	Overdue     bool   `json:"overdue"`
}

// ExpectedCents is the amount a bank deposit will match: the total including taxes, since that is
// what the tenant actually pays.
func (l Lease) ExpectedCents() int64 { return l.TotalCents }

// Client talks to one rent app. The token is a secret and is never stored in the books; the CLI
// reads it from the environment.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

type rentRoll struct {
	AsOf    string  `json:"as_of"`
	Entries []Lease `json:"entries"`
}

// Leases pulls the rent roll. Scope is active, inactive, or archived.
func (c *Client) Leases(scope string) ([]Lease, error) {
	u := c.baseURL + "/leases.json?scope=" + url.QueryEscape(scope)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rentapp: fetching the rent roll: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rentapp: rent roll returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var roll rentRoll
	if err := json.Unmarshal(body, &roll); err != nil {
		return nil, fmt.Errorf("rentapp: reading the rent roll: %w", err)
	}
	return roll.Entries, nil
}

func (c *Client) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
}
