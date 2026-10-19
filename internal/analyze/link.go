package analyze

import (
	"sort"
	"strings"
)

// Site is one place a candidate rule is decided.
type Site struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Language string `json:"language"`
	Name     string `json:"name"`
	Op       string `json:"op"`
	Value    string `json:"value"`
}

// Candidate is one concept (a name key) decided in more than one language: the same rule written
// twice, typically once in the backend and once in the user interface.
type Candidate struct {
	Key       string   `json:"key"`
	Languages []string `json:"languages"`
	Values    []string `json:"values"`
	// Drift is true when two languages share no value: the backend and the user interface may
	// disagree. It is a hint for a person to check, not a finding.
	Drift     bool   `json:"drift"`
	CoveredBy string `json:"coveredBy,omitempty"`
	Sites     []Site `json:"sites"`
}

// family groups the languages that share code and run on the same side: the JVM languages, the
// web front end (JavaScript, TypeScript and their templates), and .NET with its Razor pages.
func family(lang string) string {
	switch lang {
	case "java", "kt":
		return "jvm"
	case "ts", "js", "vue", "svelte", "html":
		return "web"
	case "cs", "razor":
		return "dotnet"
	}
	return lang
}

// link groups the hits that compare a name with a number by name key, across languages, and marks
// the hits and candidates that a project ruleset already names.
func (r *Report) link(known map[string]string) {
	keys := map[string]bool{}
	for _, h := range r.Hits {
		if h.Value != "" {
			if k := Key(h.Name); k != "" {
				keys[k] = true
			}
		}
	}
	// a key joins the shortest other key it ends with: order-quantity joins quantity
	root := func(k string) string {
		best := k
		for other := range keys {
			if len(other) < len(best) && strings.HasSuffix(k, "-"+other) {
				best = other
			}
		}
		return best
	}
	groups := map[string]*Candidate{}
	seen := map[string]bool{}
	for i := range r.Hits {
		h := &r.Hits[i]
		k := Key(h.Name)
		if k == "" {
			continue
		}
		if id := known[k]; id != "" {
			h.CoveredBy = id
		} else if id := known[root(k)]; id != "" {
			h.CoveredBy = id
		}
		if h.Value == "" {
			continue
		}
		at := h.File + ":" + itoa(h.Line)
		if seen[at] { // two detectors on one line
			continue
		}
		seen[at] = true
		g := groups[root(k)]
		if g == nil {
			g = &Candidate{Key: root(k)}
			groups[g.Key] = g
		}
		g.Sites = append(g.Sites, Site{File: h.File, Line: h.Line, Language: h.Language, Name: h.Name, Op: h.Op, Value: h.Value})
		if g.CoveredBy == "" {
			g.CoveredBy = h.CoveredBy
		}
	}
	for _, g := range groups {
		byFamily := map[string]map[string]bool{}
		langs, values := map[string]bool{}, map[string]bool{}
		for _, s := range g.Sites {
			f := family(s.Language)
			if byFamily[f] == nil {
				byFamily[f] = map[string]bool{}
			}
			byFamily[f][s.Value] = true
			langs[s.Language], values[s.Value] = true, true
		}
		if len(byFamily) < 2 {
			continue
		}
		g.Languages, g.Values = sortedKeys(langs), sortedKeys(values)
		fams := make([]string, 0, len(byFamily))
		for f := range byFamily {
			fams = append(fams, f)
		}
		for i := range fams {
			for j := i + 1; j < len(fams); j++ {
				if !overlap(byFamily[fams[i]], byFamily[fams[j]]) {
					g.Drift = true
				}
			}
		}
		r.Candidates = append(r.Candidates, *g)
	}
	sort.Slice(r.Candidates, func(i, j int) bool {
		a, b := r.Candidates[i], r.Candidates[j]
		if a.Drift != b.Drift {
			return a.Drift
		}
		return a.Key < b.Key
	})
}

func overlap(a, b map[string]bool) bool {
	for v := range a {
		if b[v] {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
