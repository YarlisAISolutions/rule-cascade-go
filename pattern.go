package rulecascade

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Portable patterns (specification section 4.4).
//
// `matches` accepts only the regular-expression syntax that means the same thing in every engine a
// runtime is likely to use. patternProblem is the scanner that decides; it is a line-by-line port
// of pattern_problem in the reference implementation.

const (
	syntaxChars      = `^$\.*+?()[]{}|/`
	classEscapes     = "dDwW"
	charEscapes      = "tnr"
	maxRepeat        = 1000
	maxPatternLength = 1000 // code points
)

type classItem struct {
	kind byte // 'c' a character, 's' a set such as \d, '-' an unescaped dash
	char rune
}

// patternProblem returns "" when the pattern is inside the portable subset, otherwise the reason
// it is not.
func patternProblem(pattern string) string {
	p := []rune(pattern)
	n := len(p)
	if n > maxPatternLength {
		return fmt.Sprintf("a pattern has at most %d characters", maxPatternLength)
	}
	in := func(set string, i int) bool { return i < n && strings.ContainsRune(set, p[i]) }

	i, depth, atom := 0, 0, false
	// Counted repetitions multiply when nested: (a{30}){40} is 1200 copies of a. The last element
	// of copies is the largest such product inside the group being read, last the one of the atom
	// just read.
	copies, last := []int{1}, 1
	// A group that can repeat more than once must not contain an unbounded quantifier: (a+)+
	// backtracks exponentially. The last element of unbounded is whether the group being read
	// contains one, grouped whether the atom just read is a group that does.
	unbounded, grouped := []bool{false}, false
	for i < n {
		c := p[i]
		switch {
		case c == '\\':
			if !in(classEscapes, i+1) && !in(charEscapes, i+1) && !in(syntaxChars, i+1) {
				return "escape \\" + string(p[min(i+1, n):min(i+2, n)]) + " is not portable"
			}
			i, atom, last, grouped = i+2, true, 1, false
		case c == '[':
			i++
			if i < n && p[i] == '^' {
				i++
			}
			var items []classItem
			for {
				if i >= n {
					return "unterminated character class"
				}
				c = p[i]
				if c == ']' {
					break
				}
				if c == '[' {
					return "'[' inside a character class must be escaped"
				}
				if c == '&' && i+1 < n && p[i+1] == '&' {
					return "'&&' inside a character class is not portable"
				}
				if c != '\\' {
					kind := byte('c')
					if c == '-' {
						kind = '-'
					}
					items = append(items, classItem{kind, c})
					i++
					continue
				}
				switch {
				case in(classEscapes, i+1):
					items = append(items, classItem{'s', p[i+1]})
				case in(charEscapes, i+1):
					items = append(items, classItem{'c', map[rune]rune{'t': '\t', 'n': '\n', 'r': '\r'}[p[i+1]]})
				case in(syntaxChars, i+1) || (i+1 < n && p[i+1] == '-'):
					items = append(items, classItem{'c', p[i+1]})
				default:
					return "escape \\" + string(p[min(i+1, n):min(i+2, n)]) + " is not portable"
				}
				i += 2
			}
			if len(items) == 0 {
				return "empty character class"
			}
			for k := 0; k < len(items); {
				switch {
				case k+2 < len(items) && items[k+1].kind == '-': // a range: char - char, low to high
					low, high := items[k], items[k+2]
					if low.kind != 'c' || high.kind != 'c' || low.char > high.char {
						return "invalid range in a character class"
					}
					k += 3
				case items[k].kind == '-' && k > 0 && k < len(items)-1:
					return "a literal '-' in a character class must be escaped, first or last"
				default:
					k++
				}
			}
			i, atom, last, grouped = i+1, true, 1, false
		case c == '(':
			if i+1 < n && p[i+1] == '?' {
				if !(i+2 < n && p[i+2] == ':') {
					return "only plain (...) and non-capturing (?:...) groups are portable"
				}
				i += 2
			}
			i, depth, atom = i+1, depth+1, false
			copies = append(copies, 1)
			unbounded = append(unbounded, false)
		case c == ')':
			if depth == 0 {
				return "unbalanced ')'"
			}
			i, depth, atom = i+1, depth-1, true
			last, copies = copies[len(copies)-1], copies[:len(copies)-1]
			copies[len(copies)-1] = max(copies[len(copies)-1], last)
			grouped, unbounded = unbounded[len(unbounded)-1], unbounded[:len(unbounded)-1]
			unbounded[len(unbounded)-1] = unbounded[len(unbounded)-1] || grouped
		case c == '*' || c == '+' || c == '?' || c == '{':
			if !atom {
				return fmt.Sprintf("nothing to repeat before '%c'", c)
			}
			repeats, endless := c != '?', c != '?' // more than once; without an upper count
			if c == '{' {
				end, low, high, ok := quantifier(p, i)
				if !ok {
					return "'{' must be escaped unless it starts a quantifier"
				}
				if low > maxRepeat || (high >= 0 && (high > maxRepeat || high < low)) {
					return fmt.Sprintf("repetition counts must be ordered and at most %d", maxRepeat)
				}
				count := high // the maximum, or the minimum where there is none; zero counts as one
				if count < 0 {
					count = low
				}
				count = max(count, 1)
				if last*count > maxRepeat {
					return fmt.Sprintf("nested repetition counts multiply to more than %d", maxRepeat)
				}
				copies[len(copies)-1] = max(copies[len(copies)-1], last*count)
				i = end
				repeats, endless = high < 0 || high > 1, high < 0
			} else {
				i++
			}
			if grouped && repeats {
				return "a group that repeats must not contain an unbounded quantifier (*, + or {n,})"
			}
			unbounded[len(unbounded)-1] = unbounded[len(unbounded)-1] || endless
			if i < n && p[i] == '?' { // lazy
				i++
			}
			atom = false
		case c == '}' || c == ']':
			return fmt.Sprintf("'%c' must be escaped", c)
		case c == '|' || c == '^' || c == '$':
			i, atom = i+1, false
		default:
			i, atom, last, grouped = i+1, true, 1, false
		}
	}
	if depth > 0 {
		return "unbalanced '('"
	}
	return ""
}

// quantifier reads {n}, {n,} or {n,m} at p[i], with at most four digits per count. high is -1
// when there is no upper bound.
func quantifier(p []rune, i int) (end, low, high int, ok bool) {
	number := func(least int) (value, count int) {
		for count < 4 && i < len(p) && p[i] >= '0' && p[i] <= '9' {
			value, count, i = value*10+int(p[i]-'0'), count+1, i+1
		}
		if count < least {
			count = -1
		}
		return value, count
	}
	i++ // the brace
	low, count := number(1)
	if count < 0 {
		return 0, 0, 0, false
	}
	high = low
	if i < len(p) && p[i] == ',' {
		i++
		if high, count = number(0); count == 0 {
			high = -1
		}
	}
	if i >= len(p) || p[i] != '}' {
		return 0, 0, 0, false
	}
	return i + 1, low, high, true
}

var patterns = struct {
	sync.Mutex
	compiled map[string]*regexp.Regexp
}{compiled: map[string]*regexp.Regexp{}}

// compilePattern returns the matcher for a portable pattern. Go's RE2 already gives the portable
// meaning to everything in the subset once '.' is made to match line breaks: \d and \w are ASCII,
// and without the m flag '^' and '$' match only at the very start and the very end.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	patterns.Lock()
	defer patterns.Unlock()
	if re, ok := patterns.compiled[pattern]; ok {
		return re, nil
	}
	if why := patternProblem(pattern); why != "" {
		return nil, fmt.Errorf("pattern %q: %s", pattern, why)
	}
	re, err := regexp.Compile("(?s)" + pattern)
	if err != nil {
		return nil, fmt.Errorf("pattern %q: %v", pattern, err)
	}
	if len(patterns.compiled) >= 1024 { // patterns are literals in a ruleset; this only bounds misuse
		patterns.compiled = map[string]*regexp.Regexp{}
	}
	patterns.compiled[pattern] = re
	return re, nil
}
