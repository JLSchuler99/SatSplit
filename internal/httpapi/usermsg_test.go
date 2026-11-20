package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"satsplit/internal/store"
	"satsplit/internal/vision"
)

func TestReadOutcomeMessage(t *testing.T) {
	tests := []struct {
		name    string
		issue   string
		checked bool
		diff    int64
		cur     string
		reading bool
		want    string
	}{
		{
			name:  "no items",
			issue: store.ReadNoItems,
			want:  msgNoItems,
		},
		{
			name:  "timeout",
			issue: store.ReadTimeout,
			want:  msgReadTimeout,
		},
		{
			name:  "reader down",
			issue: store.ReadUnavailable,
			want:  msgReaderDown,
		},
		{
			name:    "short of the total",
			checked: true,
			diff:    -10000,
			cur:     "USD",
			want:    "The items we read add up to $100.00 less than the printed total. Compare them with the photo and correct anything that's wrong before you share.",
		},
		{
			name:    "over the total",
			checked: true,
			diff:    250,
			cur:     "EUR",
			want:    "The items we read add up to €2.50 more than the printed total. Compare them with the photo and correct anything that's wrong before you share.",
		},
		{
			name:    "still reading",
			issue:   store.ReadNoItems,
			reading: true,
			want:    "",
		},
		{
			name:    "balanced",
			checked: true,
			diff:    0,
			want:    "",
		},
		{
			name: "nothing to check",
			want: "",
		},
		{
			name:  "raw error is not echoed",
			issue: `vision: request failed: Post "https://api.openai.com/v1/chat/completions": context canceled`,
			want:  msgReaderDown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readOutcomeMessage(tt.issue, tt.checked, tt.diff, tt.cur, tt.reading)
			if got != tt.want {
				t.Fatalf("message = %q\nwant    %q", got, tt.want)
			}
			assertUserFacing(t, got)
		})
	}
}

func TestClassifyVisionErr(t *testing.T) {
	deadline := fmt.Errorf("vision: request failed: Post \"https://api.openai.com/v1/chat/completions\": %w", context.DeadlineExceeded)
	canceled := fmt.Errorf("vision: request failed: %w", context.Canceled)
	noItems := vision.ErrNoItems
	joined := errors.Join(vision.ErrNoItems, vision.ErrNoItems)
	down := fmt.Errorf("vision: request failed: Post \"https://api.openai.com/v1/chat/completions\": dial tcp: no such host")

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"no items", noItems, store.ReadNoItems},
		{"both passes found nothing", joined, store.ReadNoItems},
		{"deadline", deadline, store.ReadTimeout},
		{"canceled", canceled, store.ReadTimeout},
		{"provider unreachable", down, store.ReadUnavailable},
		{"nil", nil, store.ReadNoItems},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyVisionErr(tt.err); got != tt.want {
				t.Fatalf("classify(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestLightningLookupMessage(t *testing.T) {
	dns := fmt.Errorf("Get \"https://pay.example/.well-known/lnurlp/alice\": %w", &net.DNSError{
		Err: "no such host", Name: "pay.example", IsNotFound: true,
	})
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"dns", dns, msgLNUnreachable},
		{"stringified no such host", errors.New(`Get "https://pay.example/.well-known/lnurlp/alice": no such host`), msgLNUnreachable},
		{"callback missing", errors.New("lnurl callback missing"), msgLNNotServed},
		{"bad syntax", errors.New("invalid lightning address"), msgBadAddress},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lightningLookupMessage(tt.err)
			if got != tt.want {
				t.Fatalf("message = %q\nwant    %q", got, tt.want)
			}
			assertUserFacing(t, got)
			if strings.Contains(got, "pay.example") || strings.Contains(got, "alice") {
				t.Fatalf("message includes the address: %q", got)
			}
		})
	}
}

func assertUserFacing(t *testing.T, msg string) {
	t.Helper()
	if msg == "" {
		return
	}
	low := strings.ToLower(msg)
	for _, bad := range []string{
		"gpt", "openai", "llama", "http://", "https://", "context canceled",
		"context deadline", "no such host", "well-known", "stack", ".com/v1",
	} {
		if strings.Contains(low, bad) {
			t.Fatalf("message %q includes %q", msg, bad)
		}
	}
}
