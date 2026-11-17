package lightning

import (
	"net"
	"testing"
)

func TestPaymentHash(t *testing.T) {
	// Example invoice from the BOLT11 specification.
	const invoice = "lnbc1pvjluezsp5zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zygspp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdpl2pkx2ctnv5sxxmmwwd5kgetjypeh2ursdae8g6twvus8g6rfwvs8qun0dfjkxaq9qrsgq357wnc5r2ueh7ck6q93dj32dlqnls087fxdwk8qakdyafkq3yap9us6v52vjjsrvywa6rt52cm9r9zqt8r2t7mlcwspyetp5h2tztugp9lfyql"
	const want = "0001020304050607080900010203040506070809000102030405060708090102"
	got, err := PaymentHash(invoice)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("PaymentHash = %s, want %s", got, want)
	}
	if _, err := PaymentHash("not an invoice"); err == nil {
		t.Fatal("expected an error for junk input")
	}
}

func TestCleanVerifyURL(t *testing.T) {
	cases := map[string]string{
		"https://pay.example/verify/abc": "https://pay.example/verify/abc",
		"http://pay.example/verify/abc":  "",
		"javascript:alert(1)":            "",
		"":                               "",
	}
	for in, want := range cases {
		if got := cleanVerifyURL(in); got != want {
			t.Errorf("cleanVerifyURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsPublicIP(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":         true,
		"2606:4700::1111": true,
		"127.0.0.1":       false,
		"10.0.0.5":        false,
		"192.168.1.5":     false,
		"172.16.0.1":      false,
		"169.254.169.254": false,
		"100.64.0.1":      false,
		"0.0.0.0":         false,
		"::1":             false,
		"fd00::1":         false,
		"fe80::1":         false,
	}
	for in, want := range cases {
		if got := isPublicIP(net.ParseIP(in)); got != want {
			t.Errorf("isPublicIP(%s) = %v, want %v", in, got, want)
		}
	}
}
