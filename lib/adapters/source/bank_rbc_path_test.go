package source

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// The real RBC shape (from the working reference fetcher) is exercised end to end through a browser:
// a direct sign-in form, a two-step security question answered from the stored answers, then a menu
// reached by a CSS id (#gotoBusinessHref) and plain text ("Current Account") to an account that has
// no URL of its own. rbc.js types the credentials, clears 2-step by matching the question keyword, and
// the harness walks the account path. Skipped where Node or Playwright is not installed.
func TestRBCSignsInClears2StepAndWalksThePath(t *testing.T) {
	requireBrowserTests(t)
	account, err := os.ReadFile("testdata/rbc_account.html")
	if err != nil {
		t.Fatal(err)
	}

	// The direct sign-in form (RBC's secure login URL shows this when signed out), with a OneTrust
	// consent overlay covering it -- as the real page has -- so the test fails unless the harness
	// clears it before typing.
	const login = `<!doctype html><html><body>
	  <div id="onetrust-consent-sdk" style="position:fixed;top:0;left:0;right:0;bottom:0;background:rgba(0,0,0,0.6);z-index:2147483647">
	    <button id="onetrust-accept-btn-handler" type="button">Accept</button></div>
	  <form onsubmit="return false">
	  <div id="s1"><label for="u">Client Card or Username</label><input id="u" type="text">
	    <button id="next" type="button">Next</button></div>
	  <div id="s2" style="display:none"><label for="p">Password</label><input id="p" type="text">
	    <button id="signin" type="button">Sign In</button></div>
	  <script>
	    document.getElementById('next').addEventListener('click',function(){
	      document.getElementById('s1').style.display='none';document.getElementById('s2').style.display='block'})
	    document.getElementById('signin').addEventListener('click',function(){location.href='/2sv'})
	  </script></form></body></html>`
	// The 2-step security question. Continue only proceeds once an answer has been typed, so the test
	// fails unless rbc.js actually filled it.
	const twoStep = `<!doctype html><html><body>
	  <h2>Personal Verification Questions</h2>
	  <div>What was the make of my first car?</div>
	  <input id="ans" type="text">
	  <button id="cont" type="button">Continue</button>
	  <script>
	    document.getElementById('cont').addEventListener('click',function(){
	      if(document.getElementById('ans').value.trim()!==''){document.cookie='bk_session=1; path=/';location.href='/dashboard'}})
	  </script></body></html>`
	const dashboard = `<!doctype html><html><body>
	  <a href="/signout">Sign Out</a>
	  <a id="gotoBusinessHref" href="/business">Business Banking</a>
	</body></html>`
	const business = `<!doctype html><html><body>
	  <a href="/signout">Sign Out</a>
	  <a href="/account">Current Account</a>
	</body></html>`

	authed := func(r *http.Request) bool { _, err := r.Cookie("bk_session"); return err == nil }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/2sv":
			_, _ = w.Write([]byte(twoStep))
		case "/dashboard":
			_, _ = w.Write([]byte(dashboard))
		case "/business":
			_, _ = w.Write([]byte(business))
		case "/account":
			_, _ = w.Write(account)
		default:
			if authed(r) {
				_, _ = w.Write([]byte(dashboard))
			} else {
				_, _ = w.Write([]byte(login))
			}
		}
	}))
	defer srv.Close()

	creds := map[string]string{
		"username":         "test-card",
		"password":         "test-pass",
		"answer:first car": "suzuki",
	}
	out, err := execBankScript(Bank{
		Institution:     "rbc",
		LoginURL:        srv.URL,
		DefaultCurrency: "CAD",
		AccountPath:     []string{"#gotoBusinessHref", "Current Account"},
	}, creds)
	skipIfNoBrowser(t, err)
	if err != nil {
		t.Fatalf("sign-in + 2-step + navigation: %v", err)
	}

	res, err := parseBankOutput(out, "Assets:Bank:RBC", "CAD")
	if err != nil {
		t.Fatalf("parsing rbc.js output %q: %v", out, err)
	}
	if len(res.Transactions) != 5 {
		t.Fatalf("after the full flow, got %d transactions, want 5: %q", len(res.Transactions), out)
	}
	if !res.HasBalance || res.Balance.String() != "1000.00 CAD" {
		t.Errorf("balance = %s (has=%v), want 1000.00 CAD", res.Balance, res.HasBalance)
	}
}
