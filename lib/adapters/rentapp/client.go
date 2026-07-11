// Package rentapp is the connector to the rent app, the one place that knows its endpoints.
//
// The rent app is a spoke, and a destination only: bookkeeper pushes recorded rent payments to it
// so its paid/unpaid state stays current, and never imports from it (the bank statement is the
// source of truth for money). The core of bookkeeper never imports this package; only the CLI's
// push command does, which keeps the books standalone.
package rentapp

import (
	"net/http"
	"strings"
	"time"
)

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

func (c *Client) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
}
