package httpapi

import "testing"

func TestTaxShareCents(t *testing.T) {
	// 18% of a 100.00 bill is 18.00. A 25.00 order owes 4.50.
	if got := taxShareCents(1800, 2500, 10000); got != 450 {
		t.Fatalf("full item share = %d", got)
	}
	// Half of that 25.00 item owes half the tax.
	if got := taxShareCents(1800, 1250, 10000); got != 225 {
		t.Fatalf("split item share = %d", got)
	}
	if got := taxShareCents(0, 2500, 10000); got != 0 {
		t.Fatalf("no tax = %d", got)
	}
}

func TestUnitsCents(t *testing.T) {
	tests := []struct {
		name                    string
		amount, units, quantity int64
		want                    int64
	}{
		{"one of two", 173000, 1, 2, 86500},
		{"all units", 173000, 2, 2, 173000},
		{"single unit item", 12000, 1, 1, 12000},
		{"rounds to nearest cent", 1000, 1, 3, 333},
		{"two of three", 1000, 2, 3, 667},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unitsCents(tt.amount, tt.units, tt.quantity); got != tt.want {
				t.Fatalf("unitsCents = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestClaimedUnits(t *testing.T) {
	tests := []struct{ requested, quantity, want int64 }{
		{0, 2, 1}, {1, 2, 1}, {2, 2, 2}, {5, 2, 2}, {3, 0, 1}, {-1, 4, 1},
	}
	for _, tt := range tests {
		if got := claimedUnits(tt.requested, tt.quantity); got != tt.want {
			t.Fatalf("claimedUnits(%d, %d) = %d, want %d", tt.requested, tt.quantity, got, tt.want)
		}
	}
}
