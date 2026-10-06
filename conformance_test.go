package rulecascade

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The shared conformance suite, run in process. These are the files every Rule Cascade runtime
// runs (conformance/README.md); the same cases can be run over the engine protocol with
// tools/rulecheck.py conformance --engine "rule-cascade engine --conformance-operators".

const suiteDir = "../../conformance"

func readSuite(t *testing.T, rel string) *Object {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(suiteDir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseJSON(data)
	if err == nil {
		parsed, err = normalize(parsed)
	}
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return asObj(parsed)
}

func cases(t *testing.T, suite *Object, least int) []any {
	t.Helper()
	list := suite.list("cases")
	if len(list) < least {
		t.Fatalf("expected at least %d cases, found %d", least, len(list))
	}
	return list
}

// suiteInputs reads the fixture documents and the entity schemas they refer to.
func suiteInputs(t *testing.T) (suite *Object, registry map[string]any, loader SchemaLoader) {
	t.Helper()
	suite = readSuite(t, "rulesets.json")
	registry = map[string]any{}
	suite.obj("documents").each(func(id string, rel any) { registry[id] = readSuite(t, rel.(string)) })
	schemas := map[string]any{}
	suite.obj("schemaDocuments").each(func(file string, rel any) { schemas[file] = readSuite(t, rel.(string)) })
	return suite, registry, func(file string) any { return schemas[file] }
}

func publishedBundles(t *testing.T, suite *Object) map[string]*Object {
	t.Helper()
	bundles := map[string]*Object{}
	suite.obj("bundles").each(func(id string, rel any) { bundles[id] = readSuite(t, rel.(string)) })
	if len(bundles) == 0 {
		t.Fatal("the suite lists no bundles")
	}
	return bundles
}

func TestConformanceExpressions(t *testing.T) {
	operators := ConformanceOperators()
	for _, x := range cases(t, readSuite(t, "expressions.json"), 499) {
		c := asObj(x)
		env := map[string]any{}
		c.obj("env").each(func(root string, v any) { env[root] = v })
		got, err := EvaluateExpression(c.get("expr"), env, c.get("functions"), operators)
		var failure *EvalError
		switch {
		case c.get("error") == true:
			if !errors.As(err, &failure) {
				t.Errorf("%s: expected an evaluation error, got %s (error %v)", c.str("name"), show(got), err)
			}
		case err != nil:
			t.Errorf("%s: %v", c.str("name"), err)
		case !equal(got, c.get("expect")):
			t.Errorf("%s: expected %s, got %s", c.str("name"), show(c.get("expect")), show(got))
		}
	}
}

func TestConformanceProtocol(t *testing.T) {
	engine := NewEngine(ConformanceOperators())
	for _, x := range cases(t, readSuite(t, "protocol.json"), 80) {
		c := asObj(x)
		line := []byte(c.str("line"))
		if !c.has("line") {
			line = appendValue(nil, c.get("request"), "", 0)
		}
		answer := engine.HandleLine(line)
		if strings.ContainsAny(string(answer), "\r\n") {
			t.Errorf("%s: the response is not one line", c.str("name"))
		}
		parsed, err := ParseJSON(answer)
		if err != nil {
			t.Fatalf("%s: the response is not JSON: %v", c.str("name"), err)
		}
		r := asObj(parsed)
		ok := equal(r.get("ok"), c.get("ok")) && equal(r.get("id"), c.get("id")) && r.has("id") == c.has("id")
		if want := c.obj("result"); want != nil && c.get("ok") == true { // every listed member of the result
			ok = ok && r.obj("result") != nil && subset(want, r.obj("result"))
		}
		if c.get("ok") == false {
			failure := r.obj("error")
			ok = ok && failure.str("code") == c.str("error")
			if want, has := c.Get("problem"); has {
				found := false
				for _, p := range failure.list("problems") {
					found = found || asObj(p).get("code") == want
				}
				ok = ok && found
			}
		}
		if !ok {
			t.Errorf("%s: got %s", c.str("name"), answer)
		}
	}
}

// Evaluator level: read each published bundle and run the golden tests of the matching document.
func TestConformanceBundles(t *testing.T) {
	suite, registry, _ := suiteInputs(t)
	golden := 0
	for id, bundle := range publishedBundles(t, suite) {
		rs, err := FromBundle(bundle)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if rs.ID() != id || rs.Version() != bundle.str("version") || rs.Checksum() != bundle.str("checksum") {
			t.Errorf("%s: loaded as %s@%s %s", id, rs.ID(), rs.Version(), rs.Checksum())
		}
		if !equal(rs.Bundle(), bundle) {
			t.Errorf("%s: the bundle does not survive a round trip", id)
		}
		outcomes, err := rs.RunTests(asObj(registry[id]).get("tests"), ConformanceOperators())
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		for _, outcome := range outcomes {
			golden++
			if len(outcome.Failures) > 0 {
				t.Errorf("%s: %s: %s", id, outcome.Name, strings.Join(outcome.Failures, "; "))
			}
		}
	}
	if golden < 65 {
		t.Errorf("expected at least 65 golden tests, ran %d", golden)
	}
}

// A client receives one manifest, not the bundle: the client golden tests again, from the client
// manifest alone.
func TestConformanceClientManifests(t *testing.T) {
	suite, registry, _ := suiteInputs(t)
	golden := 0
	for id, bundle := range publishedBundles(t, suite) {
		rs, err := FromManifest(bundle.obj("manifests").get("client"))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if got := rs.Channels(); len(got) != 1 || got[0] != "client" || rs.ID() != id || rs.Checksum() != bundle.str("checksum") {
			t.Errorf("%s: loaded as %s with channels %v", id, rs.ID(), got)
		}
		request := map[string]any{"entity": "Thing", "operation": "create"}
		if _, err := rs.Evaluate(request, "server", nil); err == nil {
			t.Errorf("%s: a client manifest was evaluated as the server", id)
		}
		if _, err := rs.Manifest("server"); err == nil {
			t.Errorf("%s: a client manifest gave a server manifest", id)
		}
		byDefault, err := rs.Evaluate(request, "", nil) // no channel given: the only one there is
		named, _ := rs.Evaluate(request, "client", nil)
		if err != nil || !equal(byDefault.jsonValue(), named.jsonValue()) {
			t.Errorf("%s: the default channel is not the one it has: %v", id, err)
		}
		var tests []any
		for _, x := range asObj(registry[id]).list("tests") {
			if asObj(x).get("channel") == "client" {
				tests = append(tests, x)
			}
		}
		outcomes, err := rs.RunTests(tests, ConformanceOperators())
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		for _, outcome := range outcomes {
			golden++
			if len(outcome.Failures) > 0 {
				t.Errorf("%s: from the client manifest: %s: %s", id, outcome.Name, strings.Join(outcome.Failures, "; "))
			}
		}
	}
	if golden == 0 {
		t.Error("no client golden test ran")
	}
}

// Compiler level: load each fixture with the others as its registry and compare with what the
// reference implementation published.
func TestConformanceCompiler(t *testing.T) {
	suite, registry, loader := suiteInputs(t)
	bundles := publishedBundles(t, suite)
	stringsOf := func(list []any) []string {
		out := make([]string, len(list))
		for i, s := range list {
			out[i], _ = s.(string)
		}
		sort.Strings(out)
		return out
	}
	suite.obj("expect").each(func(id string, x any) {
		expect := asObj(x)
		rs, err := Load(registry[id], registry, loader)
		if err != nil {
			t.Errorf("%s: %v", id, err)
			return
		}
		if rs.Checksum() != expect.str("checksum") {
			t.Errorf("%s: checksum %s, want %s", id, rs.Checksum(), expect.str("checksum"))
		}
		sum := sha256.Sum256([]byte(canonical(rs.Bundle())))
		if got := "sha256:" + hex.EncodeToString(sum[:]); got != expect.str("bundleChecksum") {
			t.Errorf("%s: bundle checksum %s, want %s", id, got, expect.str("bundleChecksum"))
		}
		client, err := rs.Manifest("client")
		if err != nil {
			t.Fatal(err)
		}
		var rules []any
		for _, r := range client.list("rules") {
			rules = append(rules, asObj(r).get("id"))
		}
		if got, want := stringsOf(rules), stringsOf(expect.list("clientRules")); strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: client rules %v, want %v", id, got, want)
		}
		params := client.obj("params").Keys()
		sort.Strings(params)
		if want := stringsOf(expect.list("clientParams")); strings.Join(params, " ") != strings.Join(want, " ") {
			t.Errorf("%s: client params %v, want %v", id, params, want)
		}
		if !equal(rs.Bundle(), bundles[id]) {
			t.Errorf("%s: the compiled bundle is not the published one", id)
		}
		for _, channel := range []string{"server", "client"} {
			if mf, _ := rs.Manifest(channel); !equal(mf, bundles[id].obj("manifests").get(channel)) {
				t.Errorf("%s: the %s manifest is not the published one", id, channel)
			}
		}
	})
}

func TestConformanceEvaluations(t *testing.T) {
	suite, _, _ := suiteInputs(t)
	rulesets := map[string]*RuleSet{}
	for id, bundle := range publishedBundles(t, suite) {
		rs, err := FromBundle(bundle)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		rulesets[id] = rs
	}
	operators := ConformanceOperators()
	for i, x := range cases(t, readSuite(t, "evaluations.json"), 1000) {
		c := asObj(x)
		result, err := rulesets[c.str("ruleset")].Evaluate(c.get("request"), c.str("channel"), operators)
		if err != nil {
			t.Errorf("evaluation #%d: %v", i, err)
			continue
		}
		for k := range result.Findings {
			result.Findings[k].Detail = "" // runtime-specific
		}
		if got := result.jsonValue(); !equal(got, c.get("result")) {
			t.Errorf("evaluation #%d: request %s\n  got  %s\n  want %s", i, canonical(c.get("request")),
				canonical(got), canonical(c.get("result")))
		}
	}
}

// mergePatch applies an RFC 7386 JSON Merge Patch.
func mergePatch(target, patch any) any {
	p := asObj(patch)
	if p == nil {
		return patch
	}
	out := asObj(target).copy()
	p.each(func(k string, v any) {
		if v == nil {
			out.Delete(k)
		} else {
			out.Set(k, mergePatch(out.get(k), v))
		}
	})
	return out
}

func TestConformanceLoadErrors(t *testing.T) {
	suite := readSuite(t, "load-errors.json")
	schemas := suite.obj("schemaDocuments")
	loader := func(file string) any { return schemas.get(file) }
	for _, x := range cases(t, suite, 184) {
		c := asObj(x)
		parent := mergePatch(suite.obj("base").get("parent"), orDefault(c, "parent", NewObject()))
		child := mergePatch(suite.obj("base").get("child"), orDefault(c, "child", NewObject()))
		registry := map[string]any{}
		for _, doc := range []any{parent, child} {
			if id, ok := asObj(doc).obj("metadata").Get("id"); ok {
				registry[id.(string)] = doc
			}
		}
		_, err := Load(child, registry, loader)
		var failure *LoadError
		if err != nil && !errors.As(err, &failure) {
			t.Errorf("%s: %v", c.str("name"), err)
			continue
		}
		want, mustFail := c.get("expectError").(string)
		switch {
		case !mustFail && err != nil:
			t.Errorf("%s: expected a clean load, got %v", c.str("name"), failure.Codes())
		case mustFail && (err == nil || !strings.Contains(" "+strings.Join(failure.Codes(), " ")+" ", " "+want+" ")):
			t.Errorf("%s: expected %s, got %v", c.str("name"), want, err)
		}
	}
}
