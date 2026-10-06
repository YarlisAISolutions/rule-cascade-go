package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	rulecascade "rules.sdods.com/go"
)

// ConfigNames are the file names rcas looks for, in this order, in the working directory and each
// directory above it.
var ConfigNames = []string{"rcas.yaml", "rcas.yml", "rcas.json"}

// Config is rcas.yaml: where a project keeps its rules, where compiled bundles go, which
// languages load them, and the sources rules are derived from. Every member is optional.
type Config struct {
	Rcas    int `json:"rcas"`
	Project struct {
		Name     string `json:"name"`
		IDPrefix string `json:"idPrefix"`
	} `json:"project"`
	Rules struct {
		Dir     string   `json:"dir"`
		Include []string `json:"include"`
	} `json:"rules"`
	Output struct {
		Dir       string   `json:"dir"`
		Manifests []string `json:"manifests"`
	} `json:"output"`
	Languages []string `json:"languages"`
	Channels  []string `json:"channels"`
	Sources   struct {
		OpenAPI    []SchemaSource `json:"openapi"`
		JSONSchema []SchemaSource `json:"jsonSchema"`
	} `json:"sources"`
	Analyze struct {
		Include  []string `json:"include"`
		Exclude  []string `json:"exclude"`
		MaxFiles int      `json:"maxFiles"`
	} `json:"analyze"`
	MCP struct {
		ReadOnly bool `json:"readOnly"`
		Actor    struct {
			ID    string   `json:"id"`
			Roles []string `json:"roles"`
		} `json:"actor"`
	} `json:"mcp"`
	Proposals struct {
		Dir string `json:"dir"`
	} `json:"proposals"`

	// Path is the file the configuration was read from; Root is its directory, which every
	// relative path in it is resolved against.
	Path string `json:"-"`
	Root string `json:"-"`
}

// SchemaSource is an API description that rules are derived from.
type SchemaSource struct {
	Path     string   `json:"path"`
	Schemas  []string `json:"schemas"`  // OpenAPI component schemas
	Pointer  string   `json:"pointer"`  // JSON Schema: where the entity schema is, "" for the root
	Entity   string   `json:"entity"`   // the entity name, by default the schema name
	IDPrefix string   `json:"idPrefix"` // ruleset ids are <idPrefix>.<entity in kebab-case>
}

func (cfg *Config) abs(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(cfg.Root, filepath.FromSlash(p))
}

// RulesDir is where the rulesets are: RCAS_RULES_DIR, rules.dir, or "rules".
func (cfg *Config) RulesDir() string {
	if env := os.Getenv("RCAS_RULES_DIR"); env != "" {
		return env
	}
	if cfg.Rules.Dir != "" {
		return cfg.abs(cfg.Rules.Dir)
	}
	return cfg.abs("rules")
}

// OutDir is where compile --all writes: RCAS_OUT_DIR, output.dir, or "build/rules".
func (cfg *Config) OutDir() string {
	if env := os.Getenv("RCAS_OUT_DIR"); env != "" {
		return env
	}
	if cfg.Output.Dir != "" {
		return cfg.abs(cfg.Output.Dir)
	}
	return cfg.abs("build/rules")
}

// ProposalsDir is where proposals wait for review: proposals.dir, or ".rcas/proposals".
func (cfg *Config) ProposalsDir() string {
	if cfg.Proposals.Dir != "" {
		return cfg.abs(cfg.Proposals.Dir)
	}
	return cfg.abs(".rcas/proposals")
}

// errNoConfig says that no rcas.yaml was found.
var errNoConfig = errors.New("no rcas.yaml in this directory or above it (run 'rcas init', or pass --config)")

// findConfig returns the configuration file to use: --config, RCAS_CONFIG, or the first of
// ConfigNames found from the working directory upwards.
func (c *cli) findConfig() (string, error) {
	if c.configPath != "" {
		return c.path(c.configPath), nil
	}
	if env := os.Getenv("RCAS_CONFIG"); env != "" {
		return env, nil
	}
	dir, err := filepath.Abs(c.workDir())
	if err != nil {
		return "", err
	}
	for {
		for _, name := range ConfigNames {
			candidate := filepath.Join(dir, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errNoConfig
		}
		dir = parent
	}
}

// loadConfig reads the project configuration.
func (c *cli) loadConfig() (*Config, error) {
	path, err := c.findConfig()
	if err != nil {
		return nil, err
	}
	return readConfig(path)
}

// readConfig reads and checks one configuration file.
func readConfig(path string) (*Config, error) {
	doc, _, err := readDocument(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	data, err := rulecascade.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	cfg := &Config{}
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if cfg.Rcas != 1 {
		return nil, fmt.Errorf("%s: rcas must be 1 (the version of this file's format), not %d", path, cfg.Rcas)
	}
	for _, ch := range append(append([]string{}, cfg.Channels...), cfg.Output.Manifests...) {
		if ch != "client" && ch != "server" {
			return nil, fmt.Errorf("%s: unknown channel %q (client or server)", path, ch)
		}
	}
	for _, lang := range cfg.Languages {
		if _, ok := languages[lang]; !ok {
			return nil, fmt.Errorf("%s: unknown language %q (%s)", path, lang, strings.Join(languageNames(), ", "))
		}
	}
	cfg.Path, _ = filepath.Abs(path)
	cfg.Root = filepath.Dir(cfg.Path)
	return cfg, nil
}

// compileAll compiles every ruleset of the project into <out>/<id>.bundle.json and writes the
// manifests output.manifests asks for as <out>/<id>.<channel>.manifest.json.
func (c *cli) compileAll(out string) int {
	cfg, err := c.loadConfig()
	if err != nil {
		fmt.Fprintf(c.stderr, "%s compile --all: %v\n", c.program, err)
		return exitConfig
	}
	if out == "" {
		out = cfg.OutDir()
	} else {
		out = c.path(out)
	}
	files := findRulesets(cfg.RulesDir(), cfg.Rules.Include)
	if len(files) == 0 {
		fmt.Fprintf(c.stderr, "%s compile --all: no *.ruleset.* files under %s\n", c.program, cfg.RulesDir())
		return exitFindings
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return c.fail(err)
	}
	status := exitOK
	for _, path := range files {
		rs, err := c.loadFile(path)
		if err != nil {
			fmt.Fprintf(c.stderr, "%s:\n", path)
			c.fail(err)
			status = exitFindings
			continue
		}
		write := func(name string, v any) bool {
			data, err := rulecascade.MarshalIndent(v, "  ")
			if err == nil {
				err = writeFileAtomic(filepath.Join(out, name), append(data, '\n'), 0o644)
			}
			if err != nil {
				c.fail(err)
				status = exitFindings
				return false
			}
			return true
		}
		if !write(rs.ID()+".bundle.json", rs.Bundle()) {
			continue
		}
		written := []string{rs.ID() + ".bundle.json"}
		for _, channel := range cfg.Output.Manifests {
			if !contains(rs.Channels(), channel) {
				continue
			}
			mf, err := rs.Manifest(channel)
			if err != nil {
				c.fail(err)
				status = exitFindings
				continue
			}
			if write(rs.ID()+"."+channel+".manifest.json", mf) {
				written = append(written, rs.ID()+"."+channel+".manifest.json")
			}
		}
		fmt.Fprintf(c.stdout, "%s@%s  %s  -> %s\n", rs.ID(), rs.Version(), rs.Checksum(), strings.Join(written, ", "))
	}
	return status
}
