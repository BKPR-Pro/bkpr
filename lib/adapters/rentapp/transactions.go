package rentapp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Transaction is one recorded payment in the rent app: rent, a deposit, or a fee. It is richer
// than the matching bank line, since it already knows the lease and the kind, which is the reason
// to import from here rather than infer it from a deposit's memo.
type Transaction struct {
	ID          string `json:"id"`
	LeaseID     string `json:"lease_id"`
	Kind        string `json:"kind"`
	AmountCents int64  `json:"amount_cents"`
	Description string `json:"description"`
	Method      string `json:"method"`
	PaidAt      string `json:"paid_at"`
	PaidThrough string `json:"paid_through"`
	Archived    bool   `json:"archived?"`
}

type transactionList struct {
	Scope        string        `json:"scope"`
	Transactions []Transaction `json:"transactions"`
}

// Transactions pulls the recorded payments. Scope selects which set the rent app returns (e.g.
// paid); an empty scope takes its default.
func (c *Client) Transactions(scope string) ([]Transaction, error) {
	u := c.baseURL + "/transactions.json"
	if scope != "" {
		u += "?scope=" + url.QueryEscape(scope)
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.authorize(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rentapp: fetching transactions: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rentapp: transactions returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var list transactionList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("rentapp: reading transactions: %w", err)
	}
	return list.Transactions, nil
}
