package orders

import (
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/joshbarros/golang-mint/internal/platform/messaging"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		ref   string
		units int64
		bad   string
	}{
		{"COL-1", 1, ""},
		{"COL-1", 6, ""},
		{"", 1, "reference is required"},
		{"   ", 1, "reference is required"},
		{strings.Repeat("x", 41), 1, "at most 40"},
		{strings.Repeat("x", 40), 1, ""},
		{"COL-1", 0, "at least 1 unit"},
		{"COL-1", -3, "at least 1 unit"},
		{"COL-1", 7, "at most 6 units"},
		{"COL-1", 4294967297, "at most 6 units"}, // would wrap to 1 if narrowed to 32 bits
		{"COL-1", math.MaxInt64, "at most 6 units"},
	}
	for _, c := range cases {
		got := Validate(c.ref, c.units)
		if (c.bad == "") != (got == "") || (c.bad != "" && !strings.Contains(got, c.bad)) {
			t.Errorf("Validate(%q, %d) = %q, want it to contain %q", c.ref, c.units, got, c.bad)
		}
	}
}

func TestTotalIsFaceValuePlusTwelvePercentTruncated(t *testing.T) {
	for _, c := range []struct {
		price int64
		units int
		want  int64
	}{{5000, 2, 11200}, {1, 1, 1}, {7, 1, 7}, {100, 1, 112}, {5000, 4, 22400}} {
		got, err := TotalCents(c.price, c.units)
		if err != nil || got != c.want {
			t.Errorf("TotalCents(%d,%d) = %d (%v), want %d", c.price, c.units, got, err, c.want)
		}
	}
}

func TestTotalThrowsInsteadOfWrapping(t *testing.T) {
	for _, c := range []struct {
		price int64
		units int
	}{{math.MaxInt64, 2}, {math.MaxInt64 / 2, 2}, {math.MaxInt64 / 1000, 1}, {-1, 1}} {
		if _, err := TotalCents(c.price, c.units); err == nil {
			t.Errorf("TotalCents(%d,%d) must overflow", c.price, c.units)
		}
	}
}

func TestShortReasonsSayWhy(t *testing.T) {
	id := uuid.New()
	cases := map[string]string{
		"cap":   "Cap: at most 2 units per collector",
		"stock": "Short: 3 units wanted, 1 left.",
	}
	for reason, want := range cases {
		got := ShortReason(messaging.UnitsShort{OrderID: id, Reason: reason, PerCollectorLimit: 2, RequestedUnits: 3, UnitsLeft: 1})
		if !strings.Contains(got, want) {
			t.Errorf("%s: %q should contain %q", reason, got, want)
		}
	}
}
