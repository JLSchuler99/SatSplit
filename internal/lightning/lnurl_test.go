package lightning

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// trustServer lets providerClient complete TLS with an httptest server.
func trustServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	transport := providerClient.Transport.(*http.Transport)
	orig := transport.TLSClientConfig
	transport.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	t.Cleanup(func() {
		transport.TLSClientConfig = orig
		transport.CloseIdleConnections()
	})
}

// allowLoopback lets requests reach a stand-in provider on this machine.
func allowLoopback(t *testing.T) {
	t.Helper()
	orig := allowAddress
	allowAddress = func(ip net.IP) bool { return ip.IsLoopback() || orig(ip) }
	t.Cleanup(func() { allowAddress = orig })
}

// hostOf returns the "127.0.0.1:port" part of an httptest server URL.
func hostOf(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "https://")
}

func payResponse(w http.ResponseWriter, callback string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"tag":"payRequest","callback":"` + callback + `","minSendable":1000,"maxSendable":1000000000,"metadata":"[]"}`))
}

func invoiceResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"pr":"lnbc1testinvoice","verify":"https://verify.example/v1"}`))
}

func TestLNURLPayFlow(t *testing.T) {
	allowLoopback(t)
	var amount string
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/lnurlp/alice":
			payResponse(w, srv.URL+"/cb")
		case "/cb":
			amount = r.URL.Query().Get("amount")
			invoiceResponse(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	trustServer(t, srv)

	lnurl, err := FetchLNURLPay(context.Background(), "alice@"+hostOf(srv))
	if err != nil {
		t.Fatalf("FetchLNURLPay: %v", err)
	}
	invoice, err := RequestInvoice(context.Background(), lnurl, 5000, "")
	if err != nil {
		t.Fatalf("RequestInvoice: %v", err)
	}
	if invoice.PaymentRequest != "lnbc1testinvoice" || invoice.VerifyURL != "https://verify.example/v1" {
		t.Errorf("invoice = %+v", invoice)
	}
	if amount != "5000" {
		t.Errorf("callback amount = %q, want 5000", amount)
	}
}

// The callback URL comes from the provider's reply, so a plain http one must
// not be requested at all.
func TestRequestInvoiceRejectsPlainHTTPCallback(t *testing.T) {
	allowLoopback(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		invoiceResponse(w)
	}))
	defer srv.Close()

	lnurl := LNURLPayResponse{Callback: srv.URL + "/cb", MinSendable: 1000, MaxSendable: 1_000_000_000}
	if _, err := RequestInvoice(context.Background(), lnurl, 5000, ""); err == nil {
		t.Error("RequestInvoice accepted an http callback")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("http callback was requested %d time(s)", n)
	}
}

// A provider must not be able to point the server at an address inside its
// own network, neither as the Lightning address host nor as the callback.
func TestProviderRequestsRefuseNonPublicAddresses(t *testing.T) {
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/cb" {
			invoiceResponse(w)
			return
		}
		payResponse(w, srv.URL+"/cb")
	}))
	defer srv.Close()
	trustServer(t, srv)

	cases := []struct {
		name string
		call func() error
	}{
		{"address lookup", func() error {
			_, err := FetchLNURLPay(context.Background(), "alice@"+hostOf(srv))
			return err
		}},
		{"invoice callback", func() error {
			lnurl := LNURLPayResponse{Callback: srv.URL + "/cb", MinSendable: 1000, MaxSendable: 1_000_000_000}
			_, err := RequestInvoice(context.Background(), lnurl, 5000, "")
			return err
		}},
	}
	for _, c := range cases {
		hits.Store(0)
		err := c.call()
		if err == nil || !strings.Contains(err.Error(), "not public") {
			t.Errorf("%s: err = %v, want a refusal of the non-public address", c.name, err)
		}
		if n := hits.Load(); n != 0 {
			t.Errorf("%s: loopback provider was reached %d time(s)", c.name, n)
		}
	}
}

// A Lightning address on a custom domain often redirects to the provider
// that serves it (Alby, LNbits, or apex to www), so https redirects are
// followed for both the lookup and the callback.
func TestProviderRequestsFollowHTTPSRedirects(t *testing.T) {
	allowLoopback(t)
	var amount string
	var provider *httptest.Server
	provider = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/lnurlp/alice":
			payResponse(w, provider.URL+"/cb")
		case "/cb":
			amount = r.URL.Query().Get("amount")
			invoiceResponse(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	custom := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, provider.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	defer custom.Close()
	trustServer(t, provider) // httptest TLS servers share one certificate

	lnurl, err := FetchLNURLPay(context.Background(), "alice@"+hostOf(custom))
	if err != nil {
		t.Fatalf("FetchLNURLPay through a redirect: %v", err)
	}
	lnurl.Callback = custom.URL + "/cb"
	invoice, err := RequestInvoice(context.Background(), lnurl, 5000, "")
	if err != nil {
		t.Fatalf("RequestInvoice through a redirect: %v", err)
	}
	if invoice.PaymentRequest != "lnbc1testinvoice" || amount != "5000" {
		t.Errorf("invoice = %+v, amount = %q", invoice, amount)
	}
}

// Followed redirects keep the same limits as the first request: https only,
// a public address on every hop, and a bounded number of hops.
func TestProviderRedirectsStayHTTPSAndPublic(t *testing.T) {
	var reached atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		payResponse(w, "https://pay.example/cb")
	}))
	defer target.Close()
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		payResponse(w, "https://pay.example/cb")
	}))
	defer plain.Close()
	var redirectTo string
	var start *httptest.Server
	start = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		to := redirectTo
		if to == "" { // loop back to itself
			to = start.URL + r.URL.RequestURI()
		}
		http.Redirect(w, r, to, http.StatusFound)
	}))
	defer start.Close()
	trustServer(t, start)

	cases := []struct {
		name    string
		to      string
		allow   func(net.IP) bool
		wantErr string
	}{
		// Only the first server may be dialled: the hop to target is refused
		// the way a hop into a private network would be.
		{"redirect to a non-public address", target.URL + "/.well-known/lnurlp/alice", firstDialOnly(), "not public"},
		{"redirect to plain http", plain.URL + "/.well-known/lnurlp/alice", nil, "not https"},
		{"endless redirects", "", nil, "redirects"},
	}
	for _, c := range cases {
		reached.Store(0)
		redirectTo = c.to
		orig := allowAddress
		allowAddress = func(ip net.IP) bool { return ip.IsLoopback() }
		if c.allow != nil {
			allowAddress = c.allow
		}
		providerClient.Transport.(*http.Transport).CloseIdleConnections()
		_, err := FetchLNURLPay(context.Background(), "alice@"+hostOf(start))
		allowAddress = orig
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want one mentioning %q", c.name, err, c.wantErr)
		}
		if n := reached.Load(); n != 0 {
			t.Errorf("%s: redirect target was reached %d time(s)", c.name, n)
		}
	}
}

// firstDialOnly allows one connection and refuses every later one.
func firstDialOnly() func(net.IP) bool {
	var dials atomic.Int32
	return func(net.IP) bool { return dials.Add(1) == 1 }
}
