package analyze

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Markdown renders the report for a person.
func (r *Report) Markdown() string {
	var b strings.Builder
	b.WriteString("# Rule inventory\n\n")
	b.WriteString(r.Note + "\n\n")
	// paths only, relative to the project: the report is often committed, and must not name the machine
	b.WriteString("Scanned " + itoa(r.Files) + " files")
	if len(r.Paths) > 0 && !(len(r.Paths) == 1 && r.Paths[0] == ".") {
		var paths []string
		for _, p := range r.Paths {
			paths = append(paths, code(p))
		}
		b.WriteString(" in " + strings.Join(paths, ", "))
	}
	b.WriteString(".")
	if r.Truncated {
		b.WriteString(" The scan stopped early (limit reached); narrow it with --include, --module or --feature.")
	}
	if r.Baselined > 0 {
		b.WriteString(" " + itoa(r.Baselined) + " hits are in the baseline and not shown.")
	}
	if r.BelowConfidence > 0 {
		b.WriteString(" " + itoa(r.BelowConfidence) + " hits are below --min-confidence and not shown.")
	}
	b.WriteString("\n\n## API schemas (derive rules from these first)\n\n")
	if len(r.Schemas) == 0 {
		b.WriteString("None found.\n")
	} else {
		b.WriteString("| File | Kind | Schemas |\n|---|---|---|\n")
		for _, s := range r.Schemas {
			b.WriteString("| " + code(s.Path) + " | " + s.Kind + " | " + strings.Join(s.Names, ", ") + " |\n")
		}
	}
	if len(r.Modules) > 0 {
		b.WriteString("\n## Modules\n\n| Module | Build | Hits |\n|---|---|---|\n")
		for _, m := range r.Modules {
			b.WriteString("| " + code(m.Path) + " | " + m.Build + " | " + itoa(m.Hits) + " |\n")
		}
	}
	if len(r.ByFeature) > 0 {
		b.WriteString("\n## Features\n\n| Feature | Hits |\n|---|---|\n")
		for _, f := range sortedCounts(r.ByFeature) {
			b.WriteString("| " + f + " | " + itoa(r.ByFeature[f]) + " |\n")
		}
	}
	if len(r.Candidates) > 0 {
		b.WriteString("\n## Decided in more than one language\n\n" +
			"The same name compared with a number in several languages: often one rule written twice. " +
			"Drift means the languages share no value; check which one is right.\n\n" +
			"| Concept | Drift | Values | Where | Ruleset |\n|---|---|---|---|---|\n")
		for _, c := range r.Candidates {
			drift := "no"
			if c.Drift {
				drift = "**yes**"
			}
			var where []string
			for _, s := range c.Sites {
				where = append(where, code(s.File+":"+itoa(s.Line))+" "+s.Op+" "+s.Value)
			}
			b.WriteString("| " + c.Key + " | " + drift + " | " + strings.Join(c.Values, ", ") + " | " + strings.Join(where, "<br>") + " | " + c.CoveredBy + " |\n")
		}
	}
	b.WriteString("\n## Decisions in code\n\n")
	if len(r.Hits) == 0 {
		b.WriteString("None found.\n")
		return b.String()
	}
	b.WriteString("| Library | Hits |\n|---|---|\n")
	for _, l := range sortedCounts(r.ByLibrary) {
		b.WriteString("| " + l + " | " + itoa(r.ByLibrary[l]) + " |\n")
	}
	b.WriteString("\n| Where | Library | Compares | Confidence | Ruleset | Code |\n|---|---|---|---|---|---|\n")
	for _, h := range r.Hits {
		compares := ""
		if h.Value != "" {
			compares = code(h.Name + " " + h.Op + " " + h.Value)
		}
		b.WriteString("| " + code(h.File+":"+itoa(h.Line)) + " | " + h.Library + " | " + compares + " | " + confidence(h.Confidence) +
			" | " + h.CoveredBy + " | " + code(h.Snippet) + " |\n")
	}
	return b.String()
}

// code renders text as inline code in a table cell.
func code(s string) string {
	return "`" + strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "`", "'") + "`"
}

func confidence(c float64) string { return strconv.FormatFloat(c, 'f', 2, 64) }

func sortedCounts(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SARIF renders the report as SARIF 2.1.0, for GitHub code scanning and other tools that read it.
// Every hit is a note; a site of a candidate with drift is a warning.
func (r *Report) SARIF(version string) ([]byte, error) {
	type message struct {
		Text string `json:"text"`
	}
	type rule struct {
		ID               string  `json:"id"`
		Name             string  `json:"name"`
		ShortDescription message `json:"shortDescription"`
	}
	type region struct {
		StartLine int `json:"startLine"`
	}
	type artifact struct {
		URI       string `json:"uri"`
		URIBaseID string `json:"uriBaseId"`
	}
	type physical struct {
		ArtifactLocation artifact `json:"artifactLocation"`
		Region           region   `json:"region"`
	}
	type location struct {
		PhysicalLocation physical `json:"physicalLocation"`
	}
	type result struct {
		RuleID              string            `json:"ruleId"`
		Level               string            `json:"level"`
		Message             message           `json:"message"`
		Locations           []location        `json:"locations"`
		PartialFingerprints map[string]string `json:"partialFingerprints,omitempty"`
	}
	at := func(file string, line int) []location {
		return []location{{physical{artifact{file, "%SRCROOT%"}, region{line}}}}
	}
	rules := []rule{{"rcas.analyze/drift", "drift", message{"The same concept is decided with different values in different languages."}}}
	seen := map[string]bool{}
	var results []result
	for _, h := range r.Hits {
		id := "rcas.analyze/" + h.Library
		if !seen[id] {
			seen[id] = true
			rules = append(rules, rule{id, h.Library, message{"Candidate business rule found by " + h.Library + " (" + h.Kind + ")."}})
		}
		text := "Candidate business rule (" + h.Library + ", " + h.Kind + ")"
		if h.Value != "" {
			text += ": " + h.Name + " " + h.Op + " " + h.Value
		}
		if h.CoveredBy != "" {
			text += ". The ruleset " + h.CoveredBy + " names the same field or param."
		}
		results = append(results, result{id, "note", message{text}, at(h.File, h.Line), map[string]string{"rcasFingerprint/v1": h.Fingerprint}})
	}
	for _, c := range r.Candidates {
		if !c.Drift {
			continue
		}
		for _, s := range c.Sites {
			var others []string
			for _, o := range c.Sites {
				if o != s {
					others = append(others, o.File+":"+itoa(o.Line)+" ("+o.Op+" "+o.Value+")")
				}
			}
			results = append(results, result{"rcas.analyze/drift", "warning",
				message{"Possible drift: " + c.Key + " " + s.Op + " " + s.Value + " here, but " + strings.Join(others, ", ")},
				at(s.File, s.Line), nil})
		}
	}
	if results == nil {
		results = []result{}
	}
	doc := map[string]any{
		"$schema": "https://json.schemastore.org/sarif-2.1.0.json",
		"version": "2.1.0",
		"runs": []any{map[string]any{
			"tool": map[string]any{"driver": map[string]any{"name": "rcas analyze", "version": version,
				"informationUri": "https://rulescascade.com", "rules": rules}},
			"results": results,
		}},
	}
	return json.MarshalIndent(doc, "", "  ")
}
