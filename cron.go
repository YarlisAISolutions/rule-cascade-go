package rulecascade

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed five-field cron schedule: minute hour day-of-month month day-of-week.
//
// Each field is *, a number, a range a-b, a step */n, a-b/n or a/n (a to the maximum), or a
// comma-separated list of those. Day of week is 0 to 7, where 0 and 7 are both Sunday. Names (JAN,
// MON) and macros (@daily) are not supported. When day of month and day of week are both restricted,
// a day matches when either matches; a field that begins with * is not restricted. A schedule that
// can never fire, such as "0 0 30 2 *", is refused.
//
// The TypeScript, Java and Python runtimes implement the same semantics; tools/cron-cases.json is the
// table all four are tested against. A Cron is immutable and safe for concurrent use.
type Cron struct {
	expression  string
	fields      [5][]bool
	dayStar     bool
	weekdayStar bool
}

var cronNames = [5]string{"minute", "hour", "day of month", "month", "day of week"}
var cronMin = [5]int{0, 0, 1, 1, 0}
var cronMax = [5]int{59, 23, 31, 12, 7}
var cronDaysInMonth = [12]int{31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

// cronSearchYears bounds the search for the next fire time. Only reachable through a bug.
const cronSearchYears = 30

func cronDigits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	s = strings.TrimLeft(s, "0")
	if len(s) > 6 {
		return 100000, true
	}
	if s == "" {
		return 0, true
	}
	n, _ := strconv.Atoi(s)
	return min(n, 100000), true
}

func parseCronField(text string, index int) ([]bool, error) {
	name := cronNames[index]
	allowed := make([]bool, cronMax[index]+1)
	number := func(s string) (int, error) {
		n, ok := cronDigits(s)
		if !ok {
			return 0, fmt.Errorf("%s: '%s' is not a number", name, s)
		}
		if n < cronMin[index] || n > cronMax[index] {
			return 0, fmt.Errorf("%s: %d is outside %d-%d", name, n, cronMin[index], cronMax[index])
		}
		return n, nil
	}
	for _, part := range strings.Split(text, ",") {
		pieces := strings.Split(part, "/")
		if len(pieces) > 2 {
			return nil, fmt.Errorf("%s: '%s' has more than one step", name, part)
		}
		base, step := pieces[0], 1
		if len(pieces) == 2 {
			n, ok := cronDigits(pieces[1])
			if !ok || n == 0 {
				return nil, fmt.Errorf("%s: step '%s' is not a positive number", name, pieces[1])
			}
			step = n
		}
		var from, to int
		switch {
		case base == "*":
			from, to = cronMin[index], cronMax[index]
		case strings.Contains(base, "-"):
			bounds := strings.Split(base, "-")
			if len(bounds) != 2 {
				return nil, fmt.Errorf("%s: '%s' is not a range", name, base)
			}
			var err error
			if from, err = number(bounds[0]); err != nil {
				return nil, err
			}
			if to, err = number(bounds[1]); err != nil {
				return nil, err
			}
			if from > to {
				return nil, fmt.Errorf("%s: range '%s' is reversed", name, base)
			}
		default:
			n, err := number(base)
			if err != nil {
				return nil, err
			}
			from, to = n, n
			if len(pieces) == 2 {
				to = cronMax[index] // a/n runs from a to the maximum
			}
		}
		for v := from; v <= to; v += step {
			allowed[v] = true
		}
	}
	return allowed, nil
}

// ParseCron parses a five-field cron expression. The error names the field that is wrong.
func ParseCron(expression string) (*Cron, error) {
	parts := strings.FieldsFunc(expression, func(r rune) bool { return r == ' ' || r == '\t' })
	if len(parts) != 5 {
		return nil, fmt.Errorf("'%s': a cron expression has five fields (minute hour day-of-month month day-of-week), found %d", expression, len(parts))
	}
	c := &Cron{expression: expression, dayStar: strings.HasPrefix(parts[2], "*"), weekdayStar: strings.HasPrefix(parts[4], "*")}
	for i, part := range parts {
		field, err := parseCronField(part, i)
		if err != nil {
			return nil, err
		}
		c.fields[i] = field
	}
	if c.fields[4][7] {
		c.fields[4][0] = true
	}
	if !c.dayStar && c.weekdayStar {
		possible := false
		for m := 1; m <= 12 && !possible; m++ {
			for d := 1; d <= cronDaysInMonth[m-1] && c.fields[3][m]; d++ {
				if c.fields[2][d] {
					possible = true
					break
				}
			}
		}
		if !possible {
			return nil, fmt.Errorf("'%s' never fires: no chosen month has the chosen days", expression)
		}
	}
	return c, nil
}

// String returns the expression as it was given.
func (c *Cron) String() string { return c.expression }

func (c *Cron) dayMatches(t time.Time) bool {
	byDay := c.fields[2][t.Day()]
	byWeekday := c.fields[4][int(t.Weekday())]
	if c.dayStar || c.weekdayStar {
		return byDay && byWeekday
	}
	return byDay || byWeekday
}

// nextWall finds the first matching wall-clock minute strictly after the one given. Times here are
// wall clocks held in UTC, so the arithmetic never meets a daylight saving transition.
func (c *Cron) nextWall(after time.Time) (time.Time, error) {
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := after.Year() + cronSearchYears
	for t.Year() <= limit {
		switch {
		case !c.fields[3][int(t.Month())]:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !c.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case !c.fields[1][t.Hour()]:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
		case !c.fields[0][t.Minute()]:
			t = t.Add(time.Minute)
		default:
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("'%s' does not fire within %d years", c.expression, cronSearchYears)
}

// Next returns the first fire time strictly after after, on the wall clock of after's location. Seconds
// are ignored. A wall-clock time that does not exist in the location (the hour skipped in spring) is
// skipped; one that happens twice (the hour repeated in autumn) fires once, at the first occurrence.
// It returns the zero time only if the schedule cannot fire, which ParseCron already refuses.
func (c *Cron) Next(after time.Time) time.Time {
	loc := after.Location()
	from := after.Truncate(time.Minute)
	local := from.In(loc)
	wall := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), 0, 0, time.UTC)
	for {
		var err error
		if wall, err = c.nextWall(wall); err != nil {
			return time.Time{}
		}
		if at, ok := wallInstant(wall, loc); ok && at.After(from) {
			return at
		}
	}
}

// wallInstant is the earliest instant at which loc shows the wall-clock time w (held in UTC), or false
// when loc skips that time.
func wallInstant(w time.Time, loc *time.Location) (time.Time, bool) {
	same := func(t time.Time) bool {
		l := t.In(loc)
		return l.Year() == w.Year() && l.Month() == w.Month() && l.Day() == w.Day() && l.Hour() == w.Hour() && l.Minute() == w.Minute()
	}
	t := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), 0, 0, loc)
	if !same(t) {
		return time.Time{}, false
	}
	// time.Date does not say which of two instants it picks in an overlap; prefer an earlier one.
	for _, back := range []time.Duration{2 * time.Hour, time.Hour, 30 * time.Minute} {
		if earlier := t.Add(-back); same(earlier) {
			return earlier, true
		}
	}
	return t, true
}
