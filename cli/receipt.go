package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"bkpr.pro/bkpr/lib/books"
	"bkpr.pro/bkpr/lib/model"
	"bkpr.pro/bkpr/lib/store"
)

// party is one side of the document: a name and an address split into lines, so the template renders
// line breaks without any raw HTML crossing the escaping boundary.
type party struct {
	Name    string   `json:"name"`
	Address []string `json:"address,omitempty"`
}

type invoiceItem struct {
	Label  string `json:"label"`
	Amount string `json:"amount"`
}

// invoiceDoc is the whole printable document, assembled from one transaction and its entry. It holds
// only strings, so the template does no arithmetic and the rendering is a pure function of the view.
type invoiceDoc struct {
	Title     string        `json:"title"` // the banner, e.g. INVOICE
	Kind      string        `json:"kind"`  // the reference label, e.g. Invoice (as in "Invoice No")
	Biller    party         `json:"biller"`
	BillTo    party         `json:"bill_to"`
	Reference string        `json:"reference,omitempty"`
	Date      string        `json:"date"`
	Paid      bool          `json:"paid"`
	Items     []invoiceItem `json:"items"`
	Total     string        `json:"total"`
}

// buildInvoice turns one transaction and its folded entry into the document. The biller is the
// account the money moved through (its metadata is your letterhead); the bill-to is the payee, whose
// address rides on the categorized account's metadata if it has any. The line items are the
// categorized postings, shown with the statement's sign flipped so money you received reads as a
// positive charge, the way an invoice does.
func buildInvoice(tx model.Transaction, entry model.Entry, meta map[string]map[string]string, title string) invoiceDoc {
	kind := strings.ToLower(title)
	if kind == "" {
		kind = "invoice"
	}
	doc := invoiceDoc{
		Title:     strings.ToUpper(kind),
		Kind:      strings.ToUpper(kind[:1]) + kind[1:],
		Biller:    partyFor(tx.Account, tx.Account, meta),
		Reference: docReference(tx, entry),
		Date:      tx.Date.Format("January 2, 2006"),
		// Paid is a read of the entry's own clearing flag: a line off a statement is cleared, so it
		// reads PAID, while a raised invoice whose cash has not arrived is pending and does not.
		Paid: !entry.Pending,
	}

	// The bill-to is the categorized account's own metadata (a customer's name and address) when it
	// has any, falling back to the payee the bank reported.
	billToAccount := ""
	if len(entry.Postings) > 0 {
		billToAccount = entry.Postings[0].Account
	}
	doc.BillTo = partyFor(billToAccount, entry.Payee, meta)

	var total model.Amount
	summable := true
	for _, p := range entry.Postings {
		charge := p.Amount.Negate()
		doc.Items = append(doc.Items, invoiceItem{Label: itemLabel(p, meta), Amount: charge.String()})
		if total.Commodity == "" {
			total = charge
			continue
		}
		if sum, err := total.Add(charge); err == nil {
			total = sum
		} else {
			summable = false
		}
	}
	if summable && total.Commodity != "" {
		doc.Total = total.String()
	}
	return doc
}

// docReference is what the document is quoted by: the invoice or bill number when the entry carries
// one, since that is the number a reader and a payer both cite, and the line's fingerprint otherwise.
func docReference(tx model.Transaction, entry model.Entry) string {
	if entry.Invoice != "" {
		return entry.Invoice
	}
	return tx.ID
}

// partyFor reads a name and address off an account's metadata, falling back to the given name when
// the account carries none. An account with an address but no name is headed by the address's first
// line, since that line is the name written there, rather than by a raw account path.
func partyFor(account, fallbackName string, meta map[string]map[string]string) party {
	p := party{Name: fallbackName}
	m := meta[account]
	if m == nil {
		return p
	}
	name, addr := m["name"], m["address"]
	lines := []string(nil)
	if addr != "" {
		lines = strings.Split(addr, "\n")
	}
	switch {
	case name != "":
		p.Name = name
	case len(lines) > 0:
		p.Name, lines = lines[0], lines[1:]
	}
	p.Address = lines
	return p
}

// itemLabel is what one line item reads as on the document, most specific first: the note left on the
// posting, since a person writes this line's description there; then the account's own name metadata,
// the durable display name a book gives that account; and otherwise the account's leaf. Naming the
// account is what keeps two legs sharing a leaf (rent and its tax on the same unit) distinct.
func itemLabel(p model.Posting, meta map[string]map[string]string) string {
	if p.Comment != "" {
		return p.Comment
	}
	if name := meta[p.Account]["name"]; name != "" {
		return name
	}
	return leaf(p.Account)
}

// leaf is the last segment of an account path, the human label for a posting on the document.
func leaf(account string) string {
	if i := strings.LastIndex(account, ":"); i >= 0 {
		return account[i+1:]
	}
	return account
}

// invoiceTemplate mirrors fastfinance's print-transaction view: a right-aligned title and biller
// address, the reference and date, a floated PAID, the bill-to, then a plain table of line items
// with a ruled spacer above the total. The CSS is fastfinance's (uppercase faint headers, a right
// column, a floated PAID), so the document reads the same off either app.
var invoiceTemplate = template.Must(template.New("invoice").Parse(`<!doctype html>
<meta charset="utf-8">
<title>{{.Kind}} {{.Reference}}</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; color: #000; background: #fff; max-width: 42rem; margin: 2rem auto; padding: 0 1.5rem; line-height: 1.5; }
  #transaction-invoice { overflow: hidden; }
  p { margin: 0 0 0.75rem; }
  .tr { text-align: right; }
  .float-right { float: right; }
  table { width: 100%; border-collapse: collapse; clear: both; }
  th { text-transform: uppercase; font-size: 0.75rem; opacity: 0.5; border-bottom: 1px solid #ccc; }
  th, td { padding: 0.25rem; }
  @media print { body { margin: 0; } }
</style>
<div id="transaction-invoice">
  <p class="tr">
    <strong>{{.Title}}</strong><br><br>
    <strong>{{.Biller.Name}}</strong>{{range .Biller.Address}}<br>{{.}}{{end}}
  </p>

  <p>
    {{if .Reference}}{{.Kind}} No: {{.Reference}}<br>{{end}}
    Date: {{.Date}}<br>
  </p>

  <br>

  {{if .Paid}}<p class="float-right"><strong>PAID</strong></p>{{end}}

  <p>
    <strong>Bill To:</strong><br>
    {{.BillTo.Name}}{{range .BillTo.Address}}<br>{{.}}{{end}}
  </p>

  <br><br>

  <table>
    <tbody>
      {{range .Items}}<tr><td>{{.Label}}</td><td class="tr">{{.Amount}}</td></tr>{{end}}
      <tr><th colspan="2"><br></th></tr>
      <tr><td><strong>Total</strong></td><td class="tr"><strong>{{.Total}}</strong></td></tr>
    </tbody>
  </table>
</div>
`))

// renderInvoiceHTML writes the document as a self-contained HTML page: open it and print to PDF, the
// way fastfinance does, so bkpr needs no PDF library and stays stdlib-only.
func renderInvoiceHTML(w io.Writer, doc invoiceDoc) error {
	return invoiceTemplate.Execute(w, doc)
}

// renderInvoiceText writes the same document as plain text, the default: the letterhead, the
// reference and date, a PAID line, the bill-to, and the line items with a total.
func renderInvoiceText(w io.Writer, doc invoiceDoc) error {
	fmt.Fprintf(w, "%s\n\n", doc.Title)
	fmt.Fprintln(w, doc.Biller.Name)
	for _, line := range doc.Biller.Address {
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w)
	if doc.Reference != "" {
		fmt.Fprintf(w, "%s No: %s\n", doc.Kind, doc.Reference)
	}
	fmt.Fprintf(w, "Date: %s\n", doc.Date)
	if doc.Paid {
		fmt.Fprintln(w, "PAID")
	}
	fmt.Fprintf(w, "\nBill To:\n%s\n", doc.BillTo.Name)
	for _, line := range doc.BillTo.Address {
		fmt.Fprintln(w, line)
	}

	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, it := range doc.Items {
		fmt.Fprintf(tw, "  %s\t%s\n", it.Label, it.Amount)
	}
	fmt.Fprintf(tw, "  %s\t%s\n", "Total", doc.Total)
	return tw.Flush()
}

// invoiceCmd renders one transaction as a printable invoice or receipt, found by its fingerprint.
// -format picks text or html for a person, or json (the default off a terminal) for a script or an
// agent; -out writes to a file.
func receiptCmd(args []string) error {
	fs := flag.NewFlagSet("invoice", flag.ExitOnError)
	txID := fs.String("tx", "", "the transaction fingerprint to render")
	as := fs.String("as", "receipt", "the document title: invoice or receipt")
	format := fs.String("format", defaultFormat(os.Stdout, "text", "json"), "text, html, or json (default: text at a terminal, json off one)")
	out := fs.String("out", "", "write to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *txID == "" {
		return fmt.Errorf("-tx is required: which transaction should the document render?")
	}
	if *format != "text" && *format != "html" && *format != "json" {
		return fmt.Errorf("-format must be text, html, or json")
	}

	s, err := store.OpenReader(".")
	if err != nil {
		return err
	}
	defer s.Close()

	// The accrual basis, so a raised invoice or a received bill has a document before its cash moves;
	// on the cash basis it is not in the books being searched at all.
	txs, entries, err := books.LedgerBasis(s.Log, books.AccrualBasis)
	if err != nil {
		return err
	}
	meta, err := books.AccountMeta(s.Log)
	if err != nil {
		return err
	}

	for i, tx := range txs {
		if !matchesTx(tx.ID, *txID) {
			continue
		}
		doc := buildInvoice(tx, entries[i], meta, *as)
		render := renderInvoiceText
		switch *format {
		case "html":
			render = renderInvoiceHTML
		case "json":
			render = renderInvoiceJSON
		}
		return writeOut(*out, func(w io.Writer) error { return render(w, doc) })
	}
	return fmt.Errorf("invoice: no transaction %q in the books", *txID)
}

// matchesTx says whether a folded line's id is the one asked for. An accrual's line is namespaced by
// its kind ("invoice:<fingerprint>"), but the fingerprint a person holds is the bare one invoice list
// printed, so both forms find it. A bank line matches only itself.
func matchesTx(id, want string) bool {
	return id == want || id == "invoice:"+want || id == "bill:"+want
}

// renderInvoiceJSON is the document's machine-readable form: the same fields the template renders,
// so an agent filing a document elsewhere reads structured fields rather than parsing text or HTML.
func renderInvoiceJSON(w io.Writer, doc invoiceDoc) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
