package rulecascade

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
)

// The engine protocol: JSON Lines over standard input and output (specification section 13).
//
// One JSON object per line in, one per line out, in the same order. It is how a program in any
// language, on any operating system, drives an engine it cannot link to, and how the conformance
// suite certifies one.

// EngineName is the name this engine reports for the `version` command.
const EngineName = "rule-cascade-go"

type protocolError struct {
	code, message string
	problems      []Problem
}

func badRequest(format string, args ...any) *protocolError {
	return &protocolError{code: "BAD_REQUEST", message: fmt.Sprintf(format, args...)}
}

// loadFailed reports a bundle or a document that was rejected.
func loadFailed(err error) *protocolError {
	var failure *LoadError
	if errors.As(err, &failure) {
		return &protocolError{"LOAD_FAILED", err.Error(), failure.Problems}
	}
	return badRequest("%v", err)
}

func (p *protocolError) value() *Object {
	o := NewObject()
	o.Set("code", p.code)
	o.Set("message", p.message)
	if p.problems != nil {
		problems := make([]any, len(p.problems))
		for i, problem := range p.problems {
			item := NewObject()
			item.Set("code", problem.Code)
			item.Set("message", problem.Message)
			if problem.Rule != "" {
				item.Set("rule", problem.Rule)
			}
			problems[i] = item
		}
		o.Set("problems", problems)
	}
	return o
}

// Engine answers engine-protocol requests and holds the rulesets loaded between them. It is not
// safe for concurrent use.
type Engine struct {
	// Version is reported as engineVersion. It defaults to the version of this package.
	Version string

	operators Operators
	rulesets  map[string]*RuleSet
}

// NewEngine returns an engine. operators are the custom operators built into it; custom operators
// cannot cross a process boundary, so rules that use any other one fail closed.
func NewEngine(operators Operators) *Engine {
	return &Engine{Version: Version, operators: operators, rulesets: map[string]*RuleSet{}}
}

// HandleLine answers one line of input with one line of output, without the line feed. It never
// fails: a line that cannot be understood is answered with an error response.
func (e *Engine) HandleLine(line []byte) []byte {
	var response *Object
	request, err := ParseJSON(line)
	if err == nil {
		request, err = normalize(request)
	}
	if err != nil {
		response = NewObject()
		response.Set("ok", false)
		response.Set("error", badRequest("not JSON: %v", err).value())
	} else {
		response = e.handle(request)
	}
	return appendValue(nil, response, "", 0)
}

// Handle answers one request, given as a JSON value, with the response object.
func (e *Engine) Handle(request any) *Object {
	r, err := normalize(request)
	if err != nil {
		r = nil
	}
	return e.handle(r)
}

func (e *Engine) handle(request any) *Object {
	response := NewObject()
	var result any
	failure := badRequest("a request is a JSON object")
	if r := asObj(request); r != nil {
		if id, ok := r.Get("id"); ok {
			response.Set("id", id)
		}
		result, failure = e.dispatch(r)
	}
	if failure != nil {
		response.Set("ok", false)
		response.Set("error", failure.value())
	} else {
		response.Set("ok", true)
		response.Set("result", result)
	}
	return response
}

// source checks the members that say which ruleset and channel a request is about, and returns
// the channel, "" when none is given. A source is an inline bundle, an inline manifest or the id of
// a loaded ruleset; a bundle wins over a manifest, and a manifest over a ruleset. Members first: a
// malformed request is BAD_REQUEST whatever rulesets are loaded.
func source(request *Object) (channel string, failure *protocolError) {
	if bundle, ok := request.Get("bundle"); ok {
		if asObj(bundle) == nil {
			return "", badRequest("'bundle' is missing or has the wrong type")
		}
	} else if manifest, ok := request.Get("manifest"); ok {
		if asObj(manifest) == nil {
			return "", badRequest("'manifest' is missing or has the wrong type")
		}
	} else if _, ok := request.get("ruleset").(string); !ok {
		return "", badRequest("'ruleset' is missing or has the wrong type")
	}
	if c := request.get("channel"); c != nil { // optional: absent and null are the same
		if channel, _ = c.(string); channel != "server" && channel != "client" {
			return "", badRequest("'channel' must be server or client")
		}
	}
	return channel, nil
}

// optional reads an optional object member: absent and null are the same.
func optional(request *Object, key string) (*Object, *protocolError) {
	value := request.get(key)
	if value != nil && asObj(value) == nil {
		return nil, badRequest("'%s' must be an object", key)
	}
	return asObj(value), nil
}

// ruleset reads the inline bundle or manifest of a request, or looks up the ruleset it names. An
// inline source is used for that request only.
func (e *Engine) ruleset(request *Object) (*RuleSet, *protocolError) {
	var rs *RuleSet
	var err error
	if bundle, ok := request.Get("bundle"); ok {
		rs, err = FromBundle(bundle)
	} else if manifest, ok := request.Get("manifest"); ok {
		rs, err = FromManifest(manifest)
	} else {
		id := request.str("ruleset")
		if rs = e.rulesets[id]; rs == nil {
			return nil, &protocolError{code: "UNKNOWN_RULESET", message: fmt.Sprintf("ruleset %s has not been loaded", id)}
		}
	}
	if err != nil {
		return nil, loadFailed(err)
	}
	return rs, nil
}

// manifestOf returns the manifest to use: the one asked for; by default the server's, or the
// only one there is.
func manifestOf(rs *RuleSet, channel string) (*Object, *protocolError) {
	mf, err := rs.Manifest(channel)
	if err != nil {
		return nil, &protocolError{code: "CHANNEL_UNAVAILABLE",
			message: fmt.Sprintf("ruleset %s was loaded without a %s manifest", rs.ID(), channel)}
	}
	return mf, nil
}

func (e *Engine) dispatch(request *Object) (any, *protocolError) {
	switch command, _ := request.get("command").(string); command {
	case "version":
		names := make([]string, 0, len(e.operators))
		for name := range e.operators {
			names = append(names, name)
		}
		sort.Strings(names)
		result := NewObject()
		result.Set("engine", EngineName)
		result.Set("engineVersion", e.Version)
		result.Set("ruleCascade", SpecVersion)
		result.Set("bundle", BundleVersion)
		result.Set("levels", []any{"evaluator", "compiler"})
		result.Set("operators", stringList(names))
		return result, nil

	case "compile":
		document := request.obj("document")
		if document == nil {
			return nil, badRequest("'document' is missing or has the wrong type")
		}
		given, failure := optional(request, "registry")
		if failure != nil {
			return nil, failure
		}
		schemas, failure := optional(request, "schemaDocuments")
		if failure != nil {
			return nil, failure
		}
		registry := map[string]any{}
		given.each(func(id string, doc any) { registry[id] = doc })
		if id, ok := document.obj("metadata").get("id").(string); ok {
			if _, listed := registry[id]; !listed {
				registry[id] = document
			}
		}
		var loader SchemaLoader
		if schemas != nil {
			loader = func(file string) any { return schemas.get(file) }
		}
		rs, err := Load(document, registry, loader)
		if err != nil {
			return nil, loadFailed(err)
		}
		return rs.Bundle(), nil

	case "load":
		need := "bundle"
		if request.has("manifest") && !request.has("bundle") {
			need = "manifest"
		}
		if request.obj(need) == nil {
			return nil, badRequest("'%s' is missing or has the wrong type", need)
		}
		rs, failure := e.ruleset(request)
		if failure != nil {
			return nil, failure
		}
		e.rulesets[rs.ID()] = rs // replaces whatever was loaded under that id
		result := NewObject()
		result.Set("ruleset", rs.ID())
		result.Set("version", rs.Version())
		result.Set("checksum", rs.Checksum())
		result.Set("channels", stringList(rs.Channels()))
		result.Set("missingOperators", stringList(rs.MissingOperators(e.operators)))
		return result, nil

	case "manifest", "evaluate":
		channel, failure := source(request)
		if failure != nil {
			return nil, failure
		}
		if command == "evaluate" {
			if why := requestProblem(request.get("request")); why != "" {
				return nil, badRequest("'request': %s", why)
			}
		}
		rs, failure := e.ruleset(request)
		if failure != nil {
			return nil, failure
		}
		mf, failure := manifestOf(rs, channel)
		if failure != nil {
			return nil, failure
		}
		if command == "manifest" {
			return mf, nil
		}
		result, err := evaluate(mf, request.get("request"), e.operators)
		if err != nil {
			return nil, badRequest("%v", err)
		}
		return result.jsonValue(), nil

	case "expression":
		expr, ok := request.Get("expr")
		if !ok {
			return nil, badRequest("'expr' is missing")
		}
		env, failure := optional(request, "env")
		if failure != nil {
			return nil, failure
		}
		functions, failure := optional(request, "functions")
		if failure != nil {
			return nil, failure
		}
		for _, root := range env.Keys() {
			if tooDeep(env.get(root), maxValueDepth) {
				return nil, badRequest("'env.%s' is nested more than %d deep", root, maxValueDepth)
			}
		}
		result, err := evaluateExpression(expr, env, functions, e.operators)
		if err != nil {
			return nil, &protocolError{code: "EVALUATION_ERROR", message: err.Error()}
		}
		return result, nil
	}
	return nil, badRequest("unknown command %s", show(request.get("command")))
}

// Serve answers requests, one per line, until the input ends. Blank lines are ignored and a line
// may be of any length. Every response is written, with its line feed, in a single Write.
func (e *Engine) Serve(r io.Reader, w io.Writer) error {
	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if _, err := w.Write(append(e.HandleLine(line), '\n')); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// ConformanceOperators returns the three custom operators every conformance runner registers
// (conformance/README.md). They exist to test the custom-operator mechanism.
func ConformanceOperators() Operators {
	one := func(name string, args []any) (any, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s takes one argument", name)
		}
		return args[0], nil
	}
	return Operators{
		"x-test-reverse": func(args []any) (any, error) {
			arg, err := one("x-test-reverse", args)
			s, ok := arg.(string)
			if err != nil || !ok {
				return nil, errors.New("x-test-reverse expects a string")
			}
			runes := []rune(s)
			for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
				runes[i], runes[j] = runes[j], runes[i]
			}
			return string(runes), nil
		},
		"x-test-sum": func(args []any) (any, error) {
			total := 0.0
			for _, arg := range args {
				n, ok := arg.(float64)
				if !ok {
					return nil, errors.New("x-test-sum expects numbers")
				}
				total += n
			}
			return total, nil
		},
		"x-luhn": func(args []any) (any, error) {
			arg, err := one("x-luhn", args)
			if err != nil {
				return nil, err
			}
			s, ok := arg.(string)
			if !ok || len(s) < 2 || !isDigits(s) {
				return false, nil
			}
			total := 0
			for i := 0; i < len(s); i++ {
				d := int(s[len(s)-1-i] - '0')
				if i%2 == 1 {
					d *= 2
				}
				if d > 9 {
					d -= 9
				}
				total += d
			}
			return total%10 == 0, nil
		},
	}
}
