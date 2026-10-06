// Package analyze inventories an existing code base for rule extraction: the API schemas rules can
// be derived from deterministically, and the places in the code that make business decisions
// (validation libraries and hand-written checks), with file and line. It reads; it does not
// interpret. Turning a hit into a rule is the work of a person or an AI agent reading the code.
package analyze

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Schema is an API description found in the code base.
type Schema struct {
	Path  string   `json:"path"`
	Kind  string   `json:"kind"`  // openapi or json-schema
	Names []string `json:"names"` // OpenAPI component schemas, or JSON Schema $defs / definitions
}

// Hit is one place in the code that decides something.
type Hit struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Language string `json:"language"`
	Library  string `json:"library"`
	Kind     string `json:"kind"` // schema, constraint, check, rule-definition
	Snippet  string `json:"snippet"`
}

// Report is the inventory.
type Report struct {
	Root      string         `json:"root"`
	Files     int            `json:"filesScanned"`
	Schemas   []Schema       `json:"schemas"`
	Hits      []Hit          `json:"hits"`
	ByLibrary map[string]int `json:"byLibrary"`
	Truncated bool           `json:"truncated,omitempty"`
	Note      string         `json:"note"`
}

// Options narrows a scan.
type Options struct {
	Include  []string // glob patterns relative to the root (** for any directories); empty = all
	Exclude  []string
	MaxFiles int // 0 = 20000
	MaxHits  int // 0 = 5000
}

type detector struct {
	library, kind string
	languages     []string // languages it applies to; empty = all
	re            *regexp.Regexp
}

var detectors = []detector{
	// TypeScript / JavaScript
	{"zod", "schema", []string{"ts", "js"}, regexp.MustCompile(`\bz\.(object|string|number|bigint|boolean|date|enum|nativeEnum|array|union|literal|tuple|record|discriminatedUnion)\(`)},
	{"joi", "schema", []string{"ts", "js"}, regexp.MustCompile(`\bJoi\.(object|string|number|array|date|boolean|alternatives)\(`)},
	{"yup", "schema", []string{"ts", "js"}, regexp.MustCompile(`\byup\.(object|string|number|array|date|mixed)\(`)},
	{"class-validator", "constraint", []string{"ts"}, regexp.MustCompile(`@(IsNotEmpty|IsEmail|IsOptional|Length|MinLength|MaxLength|Min|Max|Matches|IsIn|IsEnum|IsInt|IsPositive|ValidateIf|IsDateString)\(`)},
	{"ajv", "schema", []string{"ts", "js"}, regexp.MustCompile(`\bnew Ajv\(|ajv\.compile\(`)},
	// Python
	{"pydantic", "constraint", []string{"py"}, regexp.MustCompile(`\(BaseModel\)|\bField\(|@(field_validator|validator|model_validator|root_validator)\b|\b(constr|conint|confloat|condecimal)\(`)},
	{"marshmallow", "constraint", []string{"py"}, regexp.MustCompile(`\bfields\.(Str|String|Int|Integer|Decimal|Email|Date|DateTime|List|Nested)\(.*validate=|@validates(_schema)?\b`)},
	{"django", "constraint", []string{"py"}, regexp.MustCompile(`\b(MinValueValidator|MaxValueValidator|RegexValidator|EmailValidator)\(|def clean(_[a-z_]+)?\(self`)},
	// Java / Kotlin
	{"bean-validation", "constraint", []string{"java", "kt"}, regexp.MustCompile(`@(NotNull|NotBlank|NotEmpty|Size|Pattern|Min|Max|DecimalMin|DecimalMax|Email|Positive|PositiveOrZero|Past|Future|AssertTrue|Valid)\b`)},
	{"spring", "check", []string{"java", "kt"}, regexp.MustCompile(`implements\s+Validator\b|\bErrors\.rejectValue\(|errors\.rejectValue\(`)},
	// Go
	{"go-validator", "constraint", []string{"go"}, regexp.MustCompile("`[^`]*validate:\"[^\"]+\"[^`]*`")},
	{"ozzo-validation", "constraint", []string{"go"}, regexp.MustCompile(`\bvalidation\.(ValidateStruct|Field|Required|Length|Min|Max|Match|In)\b`)},
	// C#
	{"fluentvalidation", "constraint", []string{"cs"}, regexp.MustCompile(`\bRuleFor\(|AbstractValidator<`)},
	{"data-annotations", "constraint", []string{"cs"}, regexp.MustCompile(`\[(Required|StringLength|Range|RegularExpression|MaxLength|MinLength|EmailAddress)\b`)},
	// Ruby
	{"rails", "constraint", []string{"rb"}, regexp.MustCompile(`^\s*validates?(_[a-z_]+)?\s+:|^\s*validate\s+:`)},
	// PHP
	{"laravel", "constraint", []string{"php"}, regexp.MustCompile(`'(required|nullable|max|min|email|in|regex|numeric|integer|date)(:[^']*)?(\|[^']*)?'\s*[,\]]|Validator::make\(`)},
	// Rust
	{"validator-rs", "constraint", []string{"rs"}, regexp.MustCompile(`#\[validate\(`)},
	// Business-rule engines and decision tables already in the code
	{"rule-engine", "rule-definition", nil, regexp.MustCompile(`\b(json-logic|jsonLogic|json_logic|Drools|KieSession|DecisionTable|\.dmn\b|GoRules|zen-engine|easy-rules|RuleBook)\b`)},
	// Hand-written checks: a condition that throws or returns a validation error
	{"hand-written", "check", nil, regexp.MustCompile(`(?i)\b(throw\s+new\s+\w*(Validation|Business|Domain|Invalid|Argument|IllegalArgument|IllegalState|Rule)\w*(Exception|Error)\b|raise\s+(ValidationError|ValueError|PermissionDenied|\w*(Validation|Business|Rule|Domain)\w*Error)\b|errors\.New\("[^"]*(must|cannot|can't|not allowed|exceeds|invalid|required)[^"]*"\)|fmt\.Errorf\("[^"]*(must|cannot|not allowed|exceeds|required)[^"]*"|return\s+(BadRequest|UnprocessableEntity|Forbid)\w*\()`)},
}

var languageOf = map[string]string{
	".ts": "ts", ".tsx": "ts", ".mts": "ts", ".cts": "ts", ".js": "js", ".jsx": "js", ".mjs": "js", ".cjs": "js",
	".py": "py", ".java": "java", ".kt": "kt", ".kts": "kt", ".go": "go", ".cs": "cs", ".rb": "rb",
	".php": "php", ".rs": "rs", ".scala": "java", ".swift": "swift", ".dart": "dart",
}

var skip = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, "out": true, ".next": true, ".venv": true, "venv": true, "__pycache__": true, ".rcas": true,
	".idea": true, ".gradle": true, "bin": true, "obj": true, ".terraform": true, "coverage": true, ".cache": true,
}

const maxFileBytes = 2 << 20

// Scan walks root (a directory or a file).
func Scan(root string, opt Options) *Report {
	if opt.MaxFiles <= 0 {
		opt.MaxFiles = 20000
	}
	if opt.MaxHits <= 0 {
		opt.MaxHits = 5000
	}
	r := &Report{Root: root, Schemas: []Schema{}, Hits: []Hit{}, ByLibrary: map[string]int{},
		Note: "Schemas can be turned into baseline rulesets with 'rcas derive' (deterministic). " +
			"Hits are candidates: read the code around each one to learn what it decides, then write the rule. " +
			"Pattern matching finds where decisions are made, not what they mean."}
	info, err := os.Stat(root)
	if err != nil {
		return r
	}
	base := root
	if !info.IsDir() {
		base = filepath.Dir(root)
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if path != root && (skip[d.Name()] || strings.HasPrefix(d.Name(), ".") && d.Name() != ".github") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !selected(rel, opt) {
			return nil
		}
		if r.Files >= opt.MaxFiles {
			r.Truncated = true
			return filepath.SkipAll
		}
		r.Files++
		r.scanFile(path, rel, opt)
		return nil
	})
	sort.Slice(r.Hits, func(i, j int) bool {
		if r.Hits[i].File != r.Hits[j].File {
			return r.Hits[i].File < r.Hits[j].File
		}
		return r.Hits[i].Line < r.Hits[j].Line
	})
	sort.Slice(r.Schemas, func(i, j int) bool { return r.Schemas[i].Path < r.Schemas[j].Path })
	return r
}

func selected(rel string, opt Options) bool {
	for _, p := range opt.Exclude {
		if Glob(p, rel) {
			return false
		}
	}
	if len(opt.Include) == 0 {
		return true
	}
	for _, p := range opt.Include {
		if Glob(p, rel) {
			return true
		}
	}
	return false
}

var (
	openapiKey   = regexp.MustCompile(`(?m)^\s*"?openapi"?\s*:\s*"?3\.`)
	swaggerKey   = regexp.MustCompile(`(?m)^\s*"?swagger"?\s*:\s*"?2\.`)
	jsonSchemaID = regexp.MustCompile(`"\$schema"\s*:\s*"https?://json-schema\.org/`)
	schemaName   = regexp.MustCompile(`(?m)^ {4}([A-Za-z_][A-Za-z0-9_.-]*):\s*$`)
	jsonDefName  = regexp.MustCompile(`"(?:\$defs|definitions)"\s*:\s*\{`)
)

func (r *Report) scanFile(path, rel string, opt Options) {
	ext := strings.ToLower(filepath.Ext(path))
	name := strings.ToLower(filepath.Base(path))
	isSchemaFile := ext == ".yaml" || ext == ".yml" || ext == ".json"
	lang := languageOf[ext]
	if !isSchemaFile && lang == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxFileBytes {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.IndexByte(data, 0) >= 0 {
		return
	}
	if isSchemaFile {
		if strings.Contains(name, ".ruleset.") || name == "package.json" || name == "package-lock.json" || name == "tsconfig.json" {
			return
		}
		switch {
		case openapiKey.Match(data) || swaggerKey.Match(data):
			r.Schemas = append(r.Schemas, Schema{Path: rel, Kind: "openapi", Names: openapiSchemas(data)})
		case jsonSchemaID.Match(data) || strings.HasSuffix(name, ".schema.json"):
			r.Schemas = append(r.Schemas, Schema{Path: rel, Kind: "json-schema", Names: jsonSchemaDefs(data)})
		}
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") && lang != "rs" && lang != "cs" || strings.HasPrefix(trimmed, "*") {
			continue
		}
		for _, d := range detectors {
			if len(d.languages) > 0 && !has(d.languages, lang) {
				continue
			}
			if d.re.MatchString(text) {
				if len(r.Hits) >= opt.MaxHits {
					r.Truncated = true
					return
				}
				snippet := trimmed
				if len(snippet) > 200 {
					snippet = snippet[:200] + "..."
				}
				r.Hits = append(r.Hits, Hit{File: rel, Line: line, Language: lang, Library: d.library, Kind: d.kind, Snippet: snippet})
				r.ByLibrary[d.library]++
				break
			}
		}
	}
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// openapiSchemas lists the names under components.schemas of a YAML or JSON OpenAPI document,
// without parsing it (a scan must not fail on one odd file).
func openapiSchemas(data []byte) []string {
	text := string(data)
	var names []string
	if i := strings.Index(text, "\n  schemas:"); i >= 0 {
		rest := text[i+len("\n  schemas:"):]
		if end := regexp.MustCompile(`\n {0,2}[A-Za-z]`).FindStringIndex(rest); end != nil {
			rest = rest[:end[0]]
		}
		for _, m := range schemaName.FindAllStringSubmatch(rest, -1) {
			names = append(names, m[1])
		}
	} else if i := strings.Index(text, `"schemas"`); i >= 0 {
		names = topKeys(text[i:])
	}
	return names
}

func jsonSchemaDefs(data []byte) []string {
	text := string(data)
	if loc := jsonDefName.FindStringIndex(text); loc != nil {
		return topKeys(text[loc[0]:])
	}
	return []string{}
}

// topKeys returns the keys of the first JSON object in text.
func topKeys(text string) []string {
	start := strings.Index(text, "{")
	if start < 0 {
		return nil
	}
	var keys []string
	depth, inString, escaped := 0, false, false
	keyStart := -1
	for i := start; i < len(text); i++ {
		ch := text[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
				if depth == 1 && keyStart >= 0 {
					rest := strings.TrimLeft(text[i+1:], " \t\r\n")
					if strings.HasPrefix(rest, ":") {
						keys = append(keys, text[keyStart:i])
					}
				}
				keyStart = -1
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
			keyStart = i + 1
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return keys
			}
		}
	}
	return keys
}

// Glob matches a slash-separated path against a pattern in which * matches within one segment and
// ** matches any number of segments.
func Glob(pattern, path string) bool {
	return match(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func match(pattern, path []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			for i := 0; i <= len(path); i++ {
				if match(pattern[1:], path[i:]) {
					return true
				}
			}
			return false
		}
		if len(path) == 0 {
			return false
		}
		if ok, _ := filepath.Match(pattern[0], path[0]); !ok {
			return false
		}
		pattern, path = pattern[1:], path[1:]
	}
	return len(path) == 0
}

// Markdown renders the report for a person.
func (r *Report) Markdown() string {
	var b strings.Builder
	b.WriteString("# Rule inventory\n\n")
	b.WriteString(r.Note + "\n\n")
	b.WriteString("Scanned " + itoa(r.Files) + " files under `" + filepath.ToSlash(r.Root) + "`.")
	if r.Truncated {
		b.WriteString(" The scan stopped early (limit reached); narrow it with --include.")
	}
	b.WriteString("\n\n## API schemas (derive rules from these first)\n\n")
	if len(r.Schemas) == 0 {
		b.WriteString("None found.\n")
	} else {
		b.WriteString("| File | Kind | Schemas |\n|---|---|---|\n")
		for _, s := range r.Schemas {
			b.WriteString("| `" + s.Path + "` | " + s.Kind + " | " + strings.Join(s.Names, ", ") + " |\n")
		}
	}
	b.WriteString("\n## Decisions in code\n\n")
	if len(r.Hits) == 0 {
		b.WriteString("None found.\n")
		return b.String()
	}
	libs := make([]string, 0, len(r.ByLibrary))
	for l := range r.ByLibrary {
		libs = append(libs, l)
	}
	sort.Strings(libs)
	b.WriteString("| Library | Hits |\n|---|---|\n")
	for _, l := range libs {
		b.WriteString("| " + l + " | " + itoa(r.ByLibrary[l]) + " |\n")
	}
	b.WriteString("\n| Where | Library | Code |\n|---|---|---|\n")
	for _, h := range r.Hits {
		b.WriteString("| `" + h.File + ":" + itoa(h.Line) + "` | " + h.Library + " | `" + strings.ReplaceAll(strings.ReplaceAll(h.Snippet, "|", `\|`), "`", "'") + "` |\n")
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	if neg {
		d = append([]byte{'-'}, d...)
	}
	return string(d)
}
