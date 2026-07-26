package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"text/template"
	"time"

	"github.com/dallasread/bkpr/lib/books"
	"github.com/dallasread/bkpr/lib/model"
)

//go:embed templates/categorize_ui.html.tmpl
var categorizeUITemplateSrc string

var categorizeUITemplate = template.Must(template.New("categorize-ui").Parse(categorizeUITemplateSrc))

// categorizeUI is the review command's own decision queue rendered as a page: every line in a
// window, its category next to it, edited with the same accounts books already knows. `books
// -account Uncategorized` is the queue for what has no category at all; this is the queue for
// eyeballing (and correcting) what already does, at a glance, across a stretch of time no
// terminal table reads comfortably. With no subcommand it renders the page; `apply` reads back
// what a person (or an agent handed the export) decided.
func categorizeUI(args []string) error {
	if len(args) > 0 && args[0] == "apply" {
		return categorizeUIApply(args[1:])
	}
	return categorizeUIGenerate(args)
}

// categorizeUITxn is one row of the page: the line's own facts alongside its current category,
// split out from any tax leg riding beside it so the page edits the category without touching
// the split.
type categorizeUITxn struct {
	Fingerprint string `json:"fp"`
	Date        string `json:"date"`
	Payee       string `json:"payee"`
	Description string `json:"desc"`
	Amount      string `json:"amount"`
	Account     string `json:"account"`
	Category    string `json:"category"`
	SplitTax    string `json:"splitTax,omitempty"`
}

// categorizeUIPage is everything the template needs, folded once so the page is a pure function
// of this struct.
type categorizeUIPage struct {
	Txns       []categorizeUITxn `json:"txns"`
	Categories []string          `json:"categories"`
}

func categorizeUIGenerate(args []string) error {
	fs := flag.NewFlagSet("categorize-ui", flag.ExitOnError)
	from := fs.String("from", "", "show only lines dated on or after this (YYYY-MM-DD); default 4 months back from -to")
	to := fs.String("to", "", "show only lines dated on or before this (YYYY-MM-DD); default today")
	out := fs.String("out", "", "write to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	fromDay, toDay, err := periodBounds(*from, *to)
	if err != nil {
		return err
	}
	if toDay.IsZero() {
		toDay = time.Now()
	}
	if fromDay.IsZero() {
		fromDay = toDay.AddDate(0, -4, 0)
	}

	log, closeLog, err := openReader()
	if err != nil {
		return err
	}
	defer closeLog()

	txs, entries, err := books.Ledger(log)
	if err != nil {
		return err
	}
	doors, err := books.Doors(log)
	if err != nil {
		return err
	}

	categories := categoriesFrom(entries)
	windowTxs, windowEntries := filterByDate(fromDay, toDay, txs, entries)
	rows, err := categorizeUIRows(windowTxs, windowEntries, doors)
	if err != nil {
		return err
	}

	page := categorizeUIPage{Txns: rows, Categories: categories}
	dataJSON, err := json.Marshal(page)
	if err != nil {
		return err
	}

	view := struct {
		From     string
		To       string
		DataJSON string
	}{
		From: fromDay.Format("2006-01-02"),
		To:   toDay.Format("2006-01-02"),
		// json.Marshal already escapes '<', '>', and '&' by default, and text/template does no
		// escaping of its own, so this JSON drops straight into the <script> block unmangled.
		DataJSON: string(dataJSON),
	}

	return writeOut(*out, func(w io.Writer) error { return categorizeUITemplate.Execute(w, view) })
}

// categoriesFrom collects every account a posting has ever landed on, across the whole book
// rather than just the page's window, so the datalist offers a category the page's own dates
// happen not to show one of.
func categoriesFrom(entries []model.Entry) []string {
	seen := map[string]bool{}
	for _, e := range entries {
		for _, p := range e.Postings {
			seen[p.Account] = true
		}
	}
	categories := make([]string, 0, len(seen))
	for a := range seen {
		categories = append(categories, a)
	}
	sort.Strings(categories)
	return categories
}

// categorizeUIRows is the page's own reading of register's fold: buildRegister supplies the line
// identity, payee, amount, and source account (routing and all) the same way `register` prints
// them, so the two commands can never show a line differently. With no account pattern it returns
// exactly one row per line, in the same order, which is what lets the category -- the entry's
// first posting, what -category would set -- be zipped on by index. A second posting, the tax leg
// a taxed rule or -tax-rate split out, rides along read-only, so editing the category never
// proposes silently dropping it. Newest first, the way a person scans a page rather than a
// statement.
func categorizeUIRows(txs []model.Transaction, entries []model.Entry, doors map[string]string) ([]categorizeUITxn, error) {
	registerRows, err := buildRegister(txs, entries, doors, nil)
	if err != nil {
		return nil, err
	}
	rows := make([]categorizeUITxn, len(registerRows))
	for i, r := range registerRows {
		e := entries[i]
		row := categorizeUITxn{
			Fingerprint: r.ID,
			Date:        r.Date.Format("2006-01-02"),
			Payee:       r.Payee,
			Description: txs[i].Description,
			Amount:      r.Amount.String(),
			Account:     r.Account,
		}
		if len(e.Postings) > 0 {
			row.Category = e.Postings[0].Account
		}
		if len(e.Postings) > 1 {
			row.SplitTax = e.Postings[1].Account
		}
		rows[i] = row
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Date > rows[j].Date })
	return rows, nil
}

// categorizeUIChange is one line of the JSON the page's Export button downloads: read back by
// apply, and stable enough for a person (or an agent handed the file) to read directly.
type categorizeUIChange struct {
	Fingerprint string `json:"fingerprint"`
	NewCategory string `json:"new_category"`
}

// categorizeUIApply reads the page's export back in and asserts each changed line's new
// category, the same as running `categorize <fingerprint> -category <account>` by hand for
// every row. A line whose export carries a tax split is skipped: the export names only the one
// category a plain assertion would replace both postings with, so applying it would silently
// drop the tax leg; that line is left for `categorize -tax-rate` or a by-hand -post.
func categorizeUIApply(args []string) error {
	fs := flag.NewFlagSet("categorize-ui apply", flag.ExitOnError)
	actor := fs.String("actor", "human", "who is categorizing; the log records who decided")
	why := fs.String("why", "categorize-ui", "why these lines are categorized so; recorded with each assertion")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("categorize-ui apply takes exactly one file: the JSON categorize-ui's export button downloaded")
	}

	data, err := os.ReadFile(rest[0])
	if err != nil {
		return err
	}
	var changes []categorizeUIChange
	if err := json.Unmarshal(data, &changes); err != nil {
		return fmt.Errorf("%s is not the JSON categorize-ui exports: %w", rest[0], err)
	}
	if len(changes) == 0 {
		fmt.Println("no changes to apply")
		return nil
	}

	log, closeLog, err := openReader()
	if err != nil {
		return err
	}
	txs, entries, err := books.Ledger(log)
	closeLog()
	if err != nil {
		return err
	}
	split := map[string]bool{}
	for i, tx := range txs {
		if len(entries[i].Postings) > 1 {
			split[tx.ID] = true
		}
	}

	applied, skipped := 0, 0
	for _, c := range changes {
		if c.Fingerprint == "" || c.NewCategory == "" {
			continue
		}
		if split[c.Fingerprint] {
			fmt.Fprintf(os.Stderr, "skipping %s: has a tax split, recategorize it with categorize -tax-rate or -post so the split survives\n", c.Fingerprint)
			skipped++
			continue
		}
		if err := categorize([]string{c.Fingerprint, "-category", c.NewCategory, "-actor", *actor, "-why", *why}); err != nil {
			return fmt.Errorf("%s: %w", c.Fingerprint, err)
		}
		applied++
	}
	fmt.Printf("applied %d, skipped %d\n", applied, skipped)
	return nil
}
