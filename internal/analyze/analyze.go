// Package analyze inventories an existing code base for rule extraction: the API schemas rules can
// be derived from deterministically, and the places in the code that make business decisions
// (validation libraries, hand-written checks, named limits, branches on a status, conditions in a
// user interface), with file and line. It reads; it does not interpret. Turning a hit into a rule
// is the work of a person or an AI agent reading the code.
package analyze

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Named defaults; rcas.yaml, flags and RCAS_ANALYZE_* variables override them.
const (
	DefaultMaxFiles      = 20000
	DefaultMaxHits       = 5000
	DefaultMaxFileBytes  = 2 << 20
	DefaultMinConfidence = 0.0
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
	Kind     string `json:"kind"` // schema, constraint, check, parameter, branch, rule-definition
	Snippet  string `json:"snippet"`
	// Name, Op and Value are what the line compares, when it compares a name with a number:
	// quantity <= 10. They are read from the line, so they can be wrong.
	Name       string  `json:"name,omitempty"`
	Op         string  `json:"op,omitempty"`
	Value      string  `json:"value,omitempty"`
	Confidence float64 `json:"confidence"`
	Module     string  `json:"module,omitempty"`
	Feature    string  `json:"feature,omitempty"`
	// CoveredBy is a ruleset of the project that names the same field or param. It is a name
	// match, not proof that the rule says the same thing.
	CoveredBy   string `json:"coveredBy,omitempty"`
	Fingerprint string `json:"fingerprint"`
}

// Module is a directory with its own build file (pom.xml, build.gradle, package.json ...).
type Module struct {
	Path  string `json:"path"`
	Build string `json:"build"`
	Hits  int    `json:"hits"`
}

// Report is the inventory.
type Report struct {
	Root            string         `json:"root"`
	Paths           []string       `json:"paths"`
	Files           int            `json:"filesScanned"`
	Schemas         []Schema       `json:"schemas"`
	Hits            []Hit          `json:"hits"`
	ByLibrary       map[string]int `json:"byLibrary"`
	Modules         []Module       `json:"modules"`
	ByFeature       map[string]int `json:"byFeature,omitempty"`
	Candidates      []Candidate    `json:"candidates"`
	Baselined       int            `json:"baselined,omitempty"`
	BelowConfidence int            `json:"belowConfidence,omitempty"`
	Truncated       bool           `json:"truncated,omitempty"`
	Note            string         `json:"note"`
}

// Options narrows a scan.
type Options struct {
	Include []string // glob patterns (** for any directories); empty = all
	Exclude []string
	// Only, when not nil, limits the scan to these paths relative to the base (the files a change touched).
	Only map[string]bool
	// Features names sets of globs; a hit is labelled with the first feature (by name) it matches.
	Features map[string][]string
	// Baseline holds the fingerprints of hits already reviewed; they are left out of the report.
	Baseline map[string]bool
	// Known maps a name key (see Key) to the project ruleset that already names it.
	Known         map[string]string
	MaxFiles      int // 0 = DefaultMaxFiles
	MaxHits       int // 0 = DefaultMaxHits
	MaxFileBytes  int // 0 = DefaultMaxFileBytes
	MinConfidence float64
}

type detector struct {
	library, kind string
	languages     []string // languages it applies to; empty = all
	exts          []string // file extensions it applies to; empty = all of its languages
	confidence    float64
	re            *regexp.Regexp
	// literal: the hit needs a name compared with a number other than 0, 1 or -1
	literal bool
}

var codeLanguages = []string{"ts", "js", "py", "java", "kt", "go", "cs", "rb", "php", "rs", "swift", "dart", "cpp", "vue", "svelte", "razor"}

var detectors = []detector{
	// TypeScript / JavaScript
	{"zod", "schema", []string{"ts", "js"}, nil, 0.9, regexp.MustCompile(`\bz\.(object|string|number|bigint|boolean|date|enum|nativeEnum|array|union|literal|tuple|record|discriminatedUnion)\(`), false},
	{"joi", "schema", []string{"ts", "js"}, nil, 0.9, regexp.MustCompile(`\bJoi\.(object|string|number|array|date|boolean|alternatives)\(`), false},
	{"yup", "schema", []string{"ts", "js"}, nil, 0.9, regexp.MustCompile(`\byup\.(object|string|number|array|date|mixed)\(`), false},
	{"class-validator", "constraint", []string{"ts"}, nil, 0.9, regexp.MustCompile(`@(IsNotEmpty|IsEmail|IsOptional|Length|MinLength|MaxLength|Min|Max|Matches|IsIn|IsEnum|IsInt|IsPositive|ValidateIf|IsDateString)\(`), false},
	{"ajv", "schema", []string{"ts", "js"}, nil, 0.9, regexp.MustCompile(`\bnew Ajv\(|ajv\.compile\(`), false},
	{"react-hook-form", "constraint", []string{"ts", "js"}, nil, 0.85, regexp.MustCompile(`\bregister\(\s*["'][\w.\[\]]+["']\s*,\s*\{|\brules=\{\{`), false},
	{"react", "check", []string{"ts", "js"}, []string{".tsx", ".jsx"}, 0.5, regexp.MustCompile(`\{[^{}]*(&&|\?)\s*\(?\s*<[A-Za-z]|\b(disabled|hidden|readOnly|required)=\{[^}]*([<>]=?|===|!==|&&|\|\||!)[^}]*\}`), false},
	{"angular-forms", "constraint", []string{"ts"}, nil, 0.85, regexp.MustCompile(`\bValidators\.(required|requiredTrue|min|max|minLength|maxLength|pattern|email)\b`), false},
	{"angular", "check", []string{"html", "ts"}, nil, 0.5, regexp.MustCompile(`\*ngIf="|@if\s*\(|\[(disabled|hidden|required|readonly)\]="`), false},
	{"vue", "check", []string{"vue"}, nil, 0.5, regexp.MustCompile(`\bv-(if|else-if|show)="|:(disabled|readonly|required)="[^"]*([<>]=?|===|!==|&&|\|\||!)`), false},
	{"svelte", "check", []string{"svelte"}, nil, 0.5, regexp.MustCompile(`\{[#:](if|else if)\s|\b(disabled|readonly|required)=\{[^}]*([<>]=?|===|!==|&&|\|\||!)`), false},
	{"blazor", "check", []string{"razor"}, nil, 0.5, regexp.MustCompile(`@if\s*\(|\b(disabled|readonly)="@\(?`), false},
	{"flutter", "constraint", []string{"dart"}, nil, 0.6, regexp.MustCompile(`\bvalidator:\s*\(|\bFormBuilderValidators\.|\benabled:\s*[^,)]*([<>]=?|==|!=|&&|\|\|)`), false},
	{"swiftui", "check", []string{"swift"}, nil, 0.5, regexp.MustCompile(`\.disabled\([^)]*([<>]=?|==|!=|&&|\|\||!)`), false},
	{"compose", "check", []string{"kt"}, nil, 0.5, regexp.MustCompile(`\benabled\s*=\s*[^,)]*([<>]=?|==|!=|&&|\|\|)`), false},
	// Python
	{"pydantic", "constraint", []string{"py"}, nil, 0.9, regexp.MustCompile(`\(BaseModel\)|\bField\(|@(field_validator|validator|model_validator|root_validator)\b|\b(constr|conint|confloat|condecimal)\(`), false},
	{"marshmallow", "constraint", []string{"py"}, nil, 0.9, regexp.MustCompile(`\bfields\.(Str|String|Int|Integer|Decimal|Email|Date|DateTime|List|Nested)\(.*validate=|@validates(_schema)?\b`), false},
	{"django", "constraint", []string{"py"}, nil, 0.9, regexp.MustCompile(`\b(MinValueValidator|MaxValueValidator|RegexValidator|EmailValidator)\(|def clean(_[a-z_]+)?\(self`), false},
	// Java / Kotlin
	{"bean-validation", "constraint", []string{"java", "kt"}, nil, 0.9, regexp.MustCompile(`@(NotNull|NotBlank|NotEmpty|Size|Pattern|Min|Max|DecimalMin|DecimalMax|Email|Positive|PositiveOrZero|Past|Future|AssertTrue|Valid)\b`), false},
	{"spring", "check", []string{"java", "kt"}, nil, 0.85, regexp.MustCompile(`implements\s+Validator\b|\bErrors\.rejectValue\(|errors\.rejectValue\(`), false},
	{"spring-security", "check", []string{"java", "kt"}, nil, 0.8, regexp.MustCompile(`@(PreAuthorize|PostAuthorize|Secured|RolesAllowed)\b`), false},
	// Authorization in the other frameworks
	{"nestjs-roles", "check", []string{"ts"}, nil, 0.7, regexp.MustCompile(`@(Roles|Permissions|RequirePermissions)\(`), false},
	{"django-permission", "check", []string{"py"}, nil, 0.8, regexp.MustCompile(`@(permission_required|user_passes_test)\b|\bpermission_classes\s*=|\bhas_perm\(`), false},
	{"aspnet-authorize", "check", []string{"cs", "razor"}, nil, 0.8, regexp.MustCompile(`\[Authorize\(\s*(Roles|Policy)\s*=|\bAuthorizeAsync\(|<AuthorizeView\s+(Roles|Policy)=`), false},
	{"laravel-gate", "check", []string{"php"}, nil, 0.8, regexp.MustCompile(`\bGate::(allows|denies|authorize|define)\(|->can\(|@can\(`), false},
	{"rails-authorization", "check", []string{"rb"}, nil, 0.7, regexp.MustCompile(`\bauthorize!?\s+[@:]?\w|\bcan\?\s*\(?:|^\s*can\s+:\w+`), false},
	// Go
	{"go-validator", "constraint", []string{"go"}, nil, 0.9, regexp.MustCompile("`[^`]*validate:\"[^\"]+\"[^`]*`"), false},
	{"ozzo-validation", "constraint", []string{"go"}, nil, 0.9, regexp.MustCompile(`\bvalidation\.(ValidateStruct|Field|Required|Length|Min|Max|Match|In)\b`), false},
	// C#
	{"fluentvalidation", "constraint", []string{"cs"}, nil, 0.9, regexp.MustCompile(`\bRuleFor\(|AbstractValidator<`), false},
	{"data-annotations", "constraint", []string{"cs"}, nil, 0.9, regexp.MustCompile(`\[(Required|StringLength|Range|RegularExpression|MaxLength|MinLength|EmailAddress)\b`), false},
	// Ruby
	{"rails", "constraint", []string{"rb"}, nil, 0.9, regexp.MustCompile(`^\s*validates?(_[a-z_]+)?\s+:|^\s*validate\s+:|\berrors\.add\(:`), false},
	// PHP
	{"laravel", "constraint", []string{"php"}, nil, 0.9, regexp.MustCompile(`'(required|nullable|max|min|email|in|regex|numeric|integer|date)(:[^']*)?(\|[^']*)?'\s*[,\]]|Validator::make\(`), false},
	// Rust
	{"validator-rs", "constraint", []string{"rs"}, nil, 0.9, regexp.MustCompile(`#\[validate\(`), false},
	// Business-rule engines and decision tables already in the code
	{"rule-engine", "rule-definition", nil, nil, 0.9, regexp.MustCompile(`\b(json-logic|jsonLogic|json_logic|json-rules-engine|Drools|KieSession|DecisionTable|\.dmn\b|GoRules|zen-engine|easy-rules|RuleBook|NRules|RulesEngine|durable_rules|business_rules|Grule)\b`), false},
	{"drools", "rule-definition", []string{"drl"}, nil, 0.95, regexp.MustCompile(`^\s*rule\s+["']`), false},
	{"dmn", "rule-definition", []string{"dmn"}, nil, 0.95, regexp.MustCompile(`<(\w+:)?(decision|decisionTable)\b`), false},
	// Hand-written checks: a condition that throws or returns a validation error
	{"hand-written", "check", nil, nil, 0.75, regexp.MustCompile(`(?i)\b(throw\s+(new\s+)?\w*(Validation|Business|Domain|Invalid|Argument|IllegalArgument|IllegalState|Rule)\w*(Exception|Error)\b|throw\s+std::(invalid_argument|domain_error|out_of_range)\b|raise\s+(ValidationError|ValueError|PermissionDenied|ArgumentError|\w*(Validation|Business|Rule|Domain|Invalid)\w*(Error|Exception))\b|\bErr\(\s*\w*(Validation|Invalid|Business|Rule|Domain)\w*|\bbail!\("[^"]*(must|cannot|not allowed|exceeds|invalid|required)|\brequire\([^)]*([<>]=?|==|!=)[^)]*\)|ModelState\.AddModelError\(|errors\.New\("[^"]*(must|cannot|can't|not allowed|exceeds|invalid|required)[^"]*"\)|fmt\.Errorf\("[^"]*(must|cannot|not allowed|exceeds|required)[^"]*"|return\s+(BadRequest|UnprocessableEntity|ValidationProblem|Forbid)\w*\()`), false},
	// Named limits: MAX_QUANTITY = 10 (most languages), #define MAX_QUANTITY 10 (C, C++), and
	// declared constants in camel or Pascal case: const MaxQuantity = 10 (Go, C#), static let
	// maxQuantity = 10 (Swift), static const maxQuantity = 10 (Dart), constexpr int kMaxQuantity = 10
	{"named-limit", "parameter", codeLanguages, nil, 0.6, regexp.MustCompile(`\b[A-Z0-9_]*(MAX|MIN|LIMIT|THRESHOLD|RATE|FEE|DISCOUNT|AGE|DAYS|AMOUNT|QUANTITY|QTY)[A-Z0-9_]*\s*(:\s*[\w<>.]+\s*)?=\s*-?\d|#define\s+[A-Z0-9_]*(MAX|MIN|LIMIT|THRESHOLD)[A-Z0-9_]*\s+-?\d|\b(const|constexpr|static|final|let|val|readonly)\s+([\w<>?:]+\s+)*k?(Max|Min|max|min)[A-Z]\w*\s*(:\s*[\w<>.?]+\s*)?=\s*-?\d|\b(const|constexpr|static|final|let|val|readonly)\s+([\w<>?:]+\s+)*\w+(Limit|Threshold)\s*(:\s*[\w<>.?]+\s*)?=\s*-?\d`), false},
	// A name compared with a number: if (amount > 10000), {age >= 18 && ...}
	{"threshold", "check", codeLanguages, nil, 0.45, regexp.MustCompile(`[A-Za-z_$][\w$.]*(\(\))?\s*(<=|>=|===|!==|==|!=|<|>)\s*-?\d+(\.\d+)?\b`), true},
	// A branch on a status, tier or type constant
	// case "GOLD": (C-like, Python match), case .gold: (Swift), when "GOLD" / when :gold (Ruby), and
	// match arms "GOLD" => / Tier::Gold => (Rust, PHP, C#) and Tier.GOLD -> (Kotlin)
	{"branch", "branch", codeLanguages, nil, 0.4, regexp.MustCompile(`\bcase\s+("[^"]+"|'[^']+'|[A-Z][A-Za-z0-9_.]{2,}|\.[a-z]\w{2,})\s*(:|->|=>)|^\s*when\s+("[^"]+"|'[^']+'|:\w{3,}|[A-Z][\w:]{2,})|^\s*("[A-Z][A-Z0-9_]+"|[A-Z][\w]*(::|\.)[A-Z]\w*)\s*(\|\s*("[A-Z][A-Z0-9_]+"|[A-Z][\w]*(::|\.)[A-Z]\w*)\s*)*(=>|->)`), false},
}

// hashComment are the languages in which # starts a comment (not an attribute or a directive).
var hashComment = map[string]bool{"py": true, "rb": true, "php": true, "drl": true}

// markup languages get only the detectors that name them: an HTML page that mentions Drools is not a rule.
var markup = map[string]bool{"html": true, "dmn": true}

var languageOf = map[string]string{
	".ts": "ts", ".tsx": "ts", ".mts": "ts", ".cts": "ts", ".js": "js", ".jsx": "js", ".mjs": "js", ".cjs": "js",
	".py": "py", ".java": "java", ".kt": "kt", ".kts": "kt", ".go": "go", ".cs": "cs", ".rb": "rb",
	".php": "php", ".rs": "rs", ".scala": "java", ".swift": "swift", ".dart": "dart",
	".drl": "drl", ".dmn": "dmn",
	".c": "cpp", ".cc": "cpp", ".cpp": "cpp", ".cxx": "cpp", ".h": "cpp", ".hpp": "cpp", ".hh": "cpp",
	".vue": "vue", ".svelte": "svelte", ".html": "html", ".razor": "razor", ".cshtml": "razor",
}

var skip = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, "out": true, ".next": true, ".venv": true, "venv": true, "__pycache__": true, ".rcas": true,
	".idea": true, ".gradle": true, "bin": true, "obj": true, ".terraform": true, "coverage": true, ".cache": true,
}

// buildFiles mark a module, with the build tool they belong to.
var buildFiles = []struct{ file, build string }{
	{"pom.xml", "maven"}, {"build.gradle", "gradle"}, {"build.gradle.kts", "gradle"}, {"build.sbt", "sbt"},
	{"package.json", "npm"}, {"go.mod", "go"}, {"pyproject.toml", "python"}, {"setup.py", "python"},
	{"Cargo.toml", "cargo"}, {"*.csproj", "dotnet"}, {"*.fsproj", "dotnet"}, {"composer.json", "composer"},
	{"Gemfile", "bundler"}, {"Package.swift", "swift"}, {"pubspec.yaml", "dart"}, {"CMakeLists.txt", "cmake"},
}

// skipDir reports whether a scan leaves a directory out.
func skipDir(name string) bool {
	return skip[name] || strings.HasPrefix(name, ".") && name != ".github"
}

// Scan walks each path (a directory or a file) under base; every path in the report is relative to base.
func Scan(base string, paths []string, opt Options) *Report {
	if opt.MaxFiles <= 0 {
		opt.MaxFiles = DefaultMaxFiles
	}
	if opt.MaxHits <= 0 {
		opt.MaxHits = DefaultMaxHits
	}
	if opt.MaxFileBytes <= 0 {
		opt.MaxFileBytes = DefaultMaxFileBytes
	}
	if len(paths) == 0 {
		paths = []string{base}
	}
	r := &Report{Root: filepath.ToSlash(base), Paths: []string{}, Schemas: []Schema{}, Hits: []Hit{}, ByLibrary: map[string]int{},
		Modules: []Module{}, Candidates: []Candidate{},
		Note: "Schemas can be turned into baseline rulesets with 'rcas derive' (deterministic). " +
			"Hits are candidates: read the code around each one to learn what it decides, then write the rule. " +
			"Pattern matching finds where decisions are made, not what they mean."}
	s := &scan{r: r, opt: opt, base: base, modules: map[string]*Module{}, moduleOf: map[string]string{}, seen: map[string]bool{}}
	for _, root := range paths {
		rel, _ := filepath.Rel(base, root)
		r.Paths = append(r.Paths, filepath.ToSlash(rel))
		s.walk(root)
	}
	sort.Slice(r.Hits, func(i, j int) bool {
		a, b := r.Hits[i], r.Hits[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Library < b.Library
	})
	sort.Slice(r.Schemas, func(i, j int) bool { return r.Schemas[i].Path < r.Schemas[j].Path })
	r.link(opt.Known)
	r.finish(s.modules, opt)
	return r
}

type scan struct {
	r        *Report
	opt      Options
	base     string
	modules  map[string]*Module // by path
	moduleOf map[string]string  // directory -> module path
	seen     map[string]bool    // files already scanned (paths can overlap)
}

func (s *scan) walk(root string) {
	r, opt := s.r, s.opt
	info, err := os.Stat(root)
	if err != nil {
		return
	}
	local := root // include and exclude globs also match relative to the path given
	if !info.IsDir() {
		local = filepath.Dir(root)
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || r.Truncated {
			return nil
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(s.base, path)
		rel = filepath.ToSlash(rel)
		near, _ := filepath.Rel(local, path)
		if !d.Type().IsRegular() || s.seen[rel] || !selected(rel, filepath.ToSlash(near), opt) {
			return nil
		}
		if opt.Only != nil && !opt.Only[rel] {
			return nil
		}
		if r.Files >= opt.MaxFiles {
			r.Truncated = true
			return filepath.SkipAll
		}
		s.seen[rel] = true
		r.Files++
		s.file(path, rel)
		return nil
	})
}

func selected(rel, near string, opt Options) bool {
	for _, p := range opt.Exclude {
		if Glob(p, rel) || Glob(p, near) {
			return false
		}
	}
	if len(opt.Include) == 0 {
		return true
	}
	for _, p := range opt.Include {
		if Glob(p, rel) || Glob(p, near) {
			return true
		}
	}
	return false
}

// module returns the module a directory belongs to: the nearest directory at or above it, up to
// the base, that has a build file. The base itself is the module "." when nothing nearer is found.
func (s *scan) module(dir string) string {
	if m, ok := s.moduleOf[dir]; ok {
		return m
	}
	m := "."
	for d := dir; ; d = filepath.Dir(d) {
		rel, err := filepath.Rel(s.base, d)
		if err != nil || strings.HasPrefix(rel, "..") {
			break
		}
		if build := buildOf(d); build != "" {
			m = filepath.ToSlash(rel)
			if s.modules[m] == nil {
				s.modules[m] = &Module{Path: m, Build: build}
			}
			break
		}
		if rel == "." {
			break
		}
	}
	s.moduleOf[dir] = m
	return m
}

func buildOf(dir string) string {
	for _, b := range buildFiles {
		if strings.HasPrefix(b.file, "*") { // a project file named after the project: Shop.csproj
			if matches, _ := filepath.Glob(filepath.Join(dir, b.file)); len(matches) > 0 {
				return b.build
			}
		} else if _, err := os.Stat(filepath.Join(dir, b.file)); err == nil {
			return b.build
		}
	}
	return ""
}

// FindModules lists the modules under base: the directories with a build file.
func FindModules(base string) []Module {
	var out []Module
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if path != base && skipDir(d.Name()) {
			return filepath.SkipDir
		}
		if build := buildOf(path); build != "" {
			rel, _ := filepath.Rel(base, path)
			out = append(out, Module{Path: filepath.ToSlash(rel), Build: build})
		}
		return nil
	})
	return out
}

func (s *scan) file(path, rel string) {
	r, opt := s.r, s.opt
	ext := strings.ToLower(filepath.Ext(path))
	name := strings.ToLower(filepath.Base(path))
	isSchemaFile := ext == ".yaml" || ext == ".yml" || ext == ".json"
	lang := languageOf[ext]
	if !isSchemaFile && lang == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > int64(opt.MaxFileBytes) {
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
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	module := s.module(filepath.Dir(path))
	feature := featureOf(rel, opt.Features)
	for i, text := range lines {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") && hashComment[lang] || strings.HasPrefix(trimmed, "*") {
			continue
		}
		var f facts
		read := false
		for _, d := range detectors {
			if len(d.languages) > 0 && !has(d.languages, lang) || len(d.languages) == 0 && markup[lang] ||
				len(d.exts) > 0 && !has(d.exts, ext) || !d.re.MatchString(text) {
				continue
			}
			if !read {
				f, read = readFacts(lines, i), true
			}
			if d.literal && !f.literal() || d.literal && loopHeader.MatchString(trimmed) {
				continue
			}
			if len(r.Hits) >= opt.MaxHits {
				r.Truncated = true
				return
			}
			conf := d.confidence
			if f.Name != "" && businessWord.MatchString(f.Name) {
				conf += 0.15
			}
			conf = float64(int(min(conf, 1)*100+0.5)) / 100
			if conf < opt.MinConfidence {
				r.BelowConfidence++
				continue
			}
			snippet := trimmed
			if len(snippet) > 200 {
				snippet = snippet[:200] + "..."
			}
			h := Hit{File: rel, Line: i + 1, Language: lang, Library: d.library, Kind: d.kind, Snippet: snippet,
				Name: f.Name, Op: f.Op, Value: f.Value, Confidence: conf, Module: module, Feature: feature}
			h.Fingerprint = fingerprint(h)
			if opt.Baseline[h.Fingerprint] {
				r.Baselined++
				continue
			}
			r.Hits = append(r.Hits, h)
		}
	}
}

var (
	loopHeader   = regexp.MustCompile(`^(for|foreach)\b`)
	businessWord = regexp.MustCompile(`(?i)(price|amount|total|fee|rate|discount|limit|max|min|threshold|quantity|qty|age|score|tier|balance|credit|days|weight|count|level|premium|tax|eligib|status)`)
	spaces       = regexp.MustCompile(`\s+`)
)

// fingerprint identifies a hit across edits that move it: the file, the detector and the code, not the line.
func fingerprint(h Hit) string {
	sum := sha256.Sum256([]byte(h.File + "\x00" + h.Library + "\x00" + spaces.ReplaceAllString(h.Snippet, " ")))
	return hex.EncodeToString(sum[:8])
}

func featureOf(rel string, features map[string][]string) string {
	names := make([]string, 0, len(features))
	for n := range features {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		for _, g := range features[n] {
			if Glob(g, rel) {
				return n
			}
		}
	}
	return ""
}

// finish counts hits by library, module and feature.
func (r *Report) finish(modules map[string]*Module, opt Options) {
	for _, h := range r.Hits {
		r.ByLibrary[h.Library]++
		if m := modules[h.Module]; m != nil {
			m.Hits++
		}
		if h.Feature != "" {
			if r.ByFeature == nil {
				r.ByFeature = map[string]int{}
			}
			r.ByFeature[h.Feature]++
		}
	}
	for _, m := range modules {
		r.Modules = append(r.Modules, *m)
	}
	sort.Slice(r.Modules, func(i, j int) bool { return r.Modules[i].Path < r.Modules[j].Path })
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
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
