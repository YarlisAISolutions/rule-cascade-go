package rulecascade

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

type cronCases struct {
	Valid []struct {
		Expr  string   `json:"expr"`
		After string   `json:"after"`
		Next  []string `json:"next"`
		Why   string   `json:"why"`
	} `json:"valid"`
	Invalid []struct {
		Expr string `json:"expr"`
		Why  string `json:"why"`
	} `json:"invalid"`
}

// The table every runtime shares: tools/cron-cases.json.
func TestCronSharedTable(t *testing.T) {
	data, err := os.ReadFile("../../tools/cron-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases cronCases
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases.Valid) < 20 || len(cases.Invalid) < 20 {
		t.Fatalf("too few cases: %d valid, %d invalid", len(cases.Valid), len(cases.Invalid))
	}
	for _, c := range cases.Valid {
		t.Run(c.Why, func(t *testing.T) {
			schedule, err := ParseCron(c.Expr)
			if err != nil {
				t.Fatal(err)
			}
			after, err := time.Parse(time.RFC3339, c.After)
			if err != nil {
				t.Fatal(err)
			}
			for i, want := range c.Next {
				after = schedule.Next(after)
				if got := after.UTC().Format(time.RFC3339); got != want {
					t.Fatalf("%q fire %d: got %s, want %s", c.Expr, i, got, want)
				}
			}
		})
	}
	for _, c := range cases.Invalid {
		t.Run("refuses: "+c.Why, func(t *testing.T) {
			if _, err := ParseCron(c.Expr); err == nil {
				t.Fatalf("%q was accepted", c.Expr)
			}
		})
	}
}

// Daylight saving time: a skipped wall time is skipped, a repeated one fires once. Skipped where the
// platform has no time zone database.
func TestCronTimeZone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone database:", err)
	}
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v.In(ny)
	}
	next := func(expr, after string) string {
		c, err := ParseCron(expr)
		if err != nil {
			t.Fatal(err)
		}
		return c.Next(at(after)).UTC().Format(time.RFC3339)
	}
	for _, c := range [][3]string{
		{"0 9 * * *", "2026-01-10T00:00:00Z", "2026-01-10T14:00:00Z"},
		{"0 9 * * *", "2026-07-10T00:00:00Z", "2026-07-10T13:00:00Z"},
		{"30 2 * * *", "2026-03-08T00:00:00Z", "2026-03-09T06:30:00Z"},
		{"30 1 * * *", "2026-11-01T04:00:00Z", "2026-11-01T05:30:00Z"},
		{"30 1 * * *", "2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z"},
	} {
		if got := next(c[0], c[1]); got != c[2] {
			t.Errorf("%q after %s: got %s, want %s", c[0], c[1], got, c[2])
		}
	}
}
