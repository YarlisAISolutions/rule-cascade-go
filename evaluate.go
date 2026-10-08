package rulecascade

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Evaluation of one operation on one entity against a manifest (specification section 8). It is
// a port of evaluate.py of the reference implementation.

const (
	// EngineErrorCode is the code of the finding a rule produces when it cannot be evaluated.
	EngineErrorCode = "RULE-EVALUATION-ERROR"
	// EngineErrorMessage is the message of that finding.
	EngineErrorMessage = "This rule could not be evaluated."
)

// Result is the outcome of an evaluation. Findings, effects and commands are in the order the
// specification defines. A Result marshals to the JSON of the evaluation API.
type Result struct {
	Ruleset  string    `json:"ruleset"`
	Version  string    `json:"version"`
	Checksum string    `json:"checksum"`
	Decision string    `json:"decision"` // "allow" or "deny"
	Findings []Finding `json:"findings"`
	Effects  []Effect  `json:"effects"`
	Commands []Command `json:"commands"`
}

// Allowed reports whether the decision is "allow".
func (r *Result) Allowed() bool { return r.Decision == "allow" }

// Finding is one violated rule (specification section 8.1).
type Finding struct {
	Rule         string   `json:"rule"`
	Code         string   `json:"code"`
	Severity     string   `json:"severity"`
	Message      string   `json:"message"`
	Fields       []string `json:"fields"`
	Blocking     bool     `json:"blocking"`
	Status       string   `json:"status"`     // open, acknowledged or accepted
	Resolution   string   `json:"resolution"` // none, acknowledge or accept-risk
	Source       string   `json:"source"`
	Location     *Place   `json:"location,omitempty"`     // absent when the rule's target names no place
	AcceptableBy []string `json:"acceptableBy,omitempty"` // the acceptance roles, when the rule lists any
	Detail       string   `json:"detail,omitempty"`       // runtime-specific: why a rule could not be evaluated
}

// Place is a logical place in the user interface.
type Place struct {
	Page      string `json:"page,omitempty"`
	Screen    string `json:"screen,omitempty"`
	Section   string `json:"section,omitempty"`
	Component string `json:"component,omitempty"`
}

// Effect is a computed value (Type "value", with Value) or the state of a field (Type "state",
// with Set: property to value).
type Effect struct {
	Type  string  `json:"type"`
	Field string  `json:"field"`
	Value any     `json:"value,omitempty"`
	Set   *Object `json:"set,omitempty"`
	Rule  string  `json:"rule"`
}

// Command is something the host executes after it has persisted the change (section 8.2).
type Command struct {
	Name           string  `json:"name"`
	Type           string  `json:"type"`
	Rule           string  `json:"rule"`
	IdempotencyKey string  `json:"idempotencyKey"`
	Payload        *Object `json:"payload"`
	Ref            string  `json:"ref,omitempty"`
	hasRef         bool
}

func stringList(items []string) []any {
	list := make([]any, len(items))
	for i, s := range items {
		list[i] = s
	}
	return list
}

func (r Result) jsonValue() *Object {
	o := NewObject()
	o.Set("ruleset", r.Ruleset)
	o.Set("version", r.Version)
	o.Set("checksum", r.Checksum)
	o.Set("decision", r.Decision)
	findings, effects, commands := make([]any, len(r.Findings)), make([]any, len(r.Effects)), make([]any, len(r.Commands))
	for i, f := range r.Findings {
		findings[i] = f.jsonValue()
	}
	for i, e := range r.Effects {
		effects[i] = e.jsonValue()
	}
	for i, c := range r.Commands {
		commands[i] = c.jsonValue()
	}
	o.Set("findings", findings)
	o.Set("effects", effects)
	o.Set("commands", commands)
	return o
}

func (f Finding) jsonValue() *Object {
	o := NewObject()
	o.Set("rule", f.Rule)
	o.Set("code", f.Code)
	o.Set("severity", f.Severity)
	o.Set("message", f.Message)
	o.Set("fields", stringList(f.Fields))
	o.Set("blocking", f.Blocking)
	o.Set("status", f.Status)
	o.Set("resolution", f.Resolution)
	o.Set("source", f.Source)
	if f.Location != nil {
		place := NewObject()
		for i, v := range []string{f.Location.Page, f.Location.Screen, f.Location.Section, f.Location.Component} {
			if v != "" {
				place.Set(viewKeys[i], v)
			}
		}
		o.Set("location", place)
	}
	if len(f.AcceptableBy) > 0 {
		o.Set("acceptableBy", stringList(f.AcceptableBy))
	}
	if f.Detail != "" {
		o.Set("detail", f.Detail)
	}
	return o
}

func (e Effect) jsonValue() *Object {
	o := NewObject()
	o.Set("type", e.Type)
	o.Set("field", e.Field)
	if e.Type == "state" {
		o.Set("set", e.Set)
	} else {
		o.Set("value", e.Value)
	}
	o.Set("rule", e.Rule)
	return o
}

func (c Command) jsonValue() *Object {
	o := NewObject()
	o.Set("name", c.Name)
	o.Set("type", c.Type)
	o.Set("rule", c.Rule)
	o.Set("idempotencyKey", c.IdempotencyKey)
	o.Set("payload", c.Payload)
	if c.hasRef || c.Ref != "" {
		o.Set("ref", c.Ref)
	}
	return o
}

// MarshalJSON writes the result as the evaluation API defines it.
func (r Result) MarshalJSON() ([]byte, error) { return Marshal(r.jsonValue()) }

// MarshalJSON writes the finding as the evaluation API defines it.
func (f Finding) MarshalJSON() ([]byte, error) { return Marshal(f.jsonValue()) }

// MarshalJSON writes a value effect with its value, even when that is null, and a state effect
// with its set.
func (e Effect) MarshalJSON() ([]byte, error) { return Marshal(e.jsonValue()) }

// MarshalJSON writes the command as the evaluation API defines it.
func (c Command) MarshalJSON() ([]byte, error) { return Marshal(c.jsonValue()) }

// RequestError says that an evaluation request does not have the shape of specification section 8.
// Such a request is refused before anything is evaluated.
type RequestError struct{ Message string }

func (e *RequestError) Error() string { return "rulecascade: " + e.Message }

// fill replaces each {name} in a template by the rendered argument; unknown placeholders stay.
func fill(template string, args *Object) string {
	return placeholder.ReplaceAllStringFunc(template, func(m string) string {
		if v, ok := args.Get(m[1 : len(m)-1]); ok {
			return render(v)
		}
		return m
	})
}

func pointerPath(root, pointer string) string { return root + strings.ReplaceAll(pointer, "/", ".") }

// setPointer writes a value into the working copy of data, creating objects on the way.
func setPointer(data *Object, pointer string, value any) {
	segments := pointerSegments(pointer)
	for _, seg := range segments[:len(segments)-1] {
		if data.obj(seg) == nil {
			data.Set(seg, NewObject())
		}
		data = data.obj(seg)
	}
	data.Set(segments[len(segments)-1], value)
}

// localeChain lists the catalogs to consult, least specific first: the default locale, then every
// prefix of the requested tag that ends at a subtag. fr-CA reads en, fr, fr-CA, so a regional
// catalog only needs the messages that differ. Tags are compared exactly, including case.
//
// Prefixes longer than longest bytes are left out: given the length of the longest catalog tag,
// which no longer prefix can equal, the work no longer grows with the length of wanted. A negative
// longest leaves none out.
func localeChain(defaultLocale, wanted string, longest int) []string {
	chain := []string{defaultLocale}
	if wanted == "" {
		return chain
	}
	limit := len(wanted)
	if longest >= 0 && longest < limit {
		limit = longest
	}
	// a prefix that ends before the hyphen at index i is i long
	for i := 0; i <= limit; i++ {
		next := strings.IndexByte(wanted[i:], '-')
		if next < 0 || i+next > limit {
			break
		}
		i += next
		chain = append(chain, wanted[:i])
	}
	if len(wanted) <= limit {
		chain = append(chain, wanted)
	}
	return chain
}

// longestTag is the length of the longest catalog tag: a longer tag cannot name a catalog.
func longestTag(messages *Object) int {
	longest := 0
	for _, tag := range messages.names() {
		longest = max(longest, len(tag))
	}
	return longest
}

// requestProblem says why a value is not an evaluation request, or returns "". It is checked
// before anything is evaluated, so a malformed request is refused instead of half-evaluated.
// Optional members may be null.
func requestProblem(request any) string {
	r := asObj(request)
	if r == nil {
		return "a request is an object"
	}
	isObject := func(v any) bool { return v == nil || asObj(v) != nil }
	isList := func(v any) bool { _, ok := v.([]any); return v == nil || ok }
	isString := func(v any) bool { _, ok := v.(string); return v == nil || ok }
	for _, key := range []string{"entity", "operation"} {
		if _, ok := r.get(key).(string); !ok {
			return "'" + key + "' must be a string"
		}
	}
	for _, member := range []struct {
		key  string
		ok   func(any) bool
		what string
	}{{"data", isObject, "an object"}, {"original", isObject, "an object"}, {"actor", isObject, "an object"},
		{"ctx", isObject, "an object"}, {"view", isObject, "an object"}, {"resolutions", isList, "a list"},
		{"trigger", isString, "a string"}, {"locale", isString, "a string"}} {
		if !member.ok(r.get(member.key)) {
			return "'" + member.key + "' must be " + member.what
		}
	}
	for _, key := range []string{"data", "original", "actor", "ctx"} {
		if tooDeep(r.get(key), maxValueDepth) {
			return fmt.Sprintf("'%s' is nested more than %d deep", key, maxValueDepth)
		}
	}
	if roles := r.obj("actor").get("roles"); !isList(roles) {
		return "'actor.roles' must be a list of strings"
	}
	for _, role := range r.obj("actor").list("roles") {
		if _, ok := role.(string); !ok {
			return "'actor.roles' must be a list of strings"
		}
	}
	for _, key := range viewKeys {
		if !isString(r.obj("view").get(key)) {
			return "'view." + key + "' must be a string"
		}
	}
	for _, x := range r.list("resolutions") {
		resolution := asObj(x)
		_, hasRule := resolution.get("rule").(string)
		_, hasType := resolution.get("type").(string)
		if !hasRule || !hasType || !isString(resolution.get("justification")) {
			return "a resolution is an object with a string 'rule' and 'type' and an optional string 'justification'"
		}
	}
	return ""
}

// Evaluate evaluates one operation on one entity (specification section 8). It is pure: no I/O,
// no clock, no randomness.
//
// request is a JSON object with entity, operation and optionally data, original, actor, ctx,
// resolutions, trigger, locale and view: an *Object, a map[string]any or anything encoding/json
// can write as such an object. channel is "server" or "client"; an empty channel is the server's
// when the ruleset has it and otherwise the channel it has. operators supplies the custom
// operators the ruleset uses and may be nil.
//
// Rules that cannot be evaluated never produce an error: they produce a blocking finding. The
// error is a *RequestError when the request does not have the shape the specification requires,
// and is otherwise non-nil only for a channel the ruleset does not have.
func (rs *RuleSet) Evaluate(request any, channel string, operators Operators) (*Result, error) {
	mf, err := rs.Manifest(channel)
	if err != nil {
		return nil, err
	}
	req, err := normalize(request)
	if err != nil {
		return nil, err
	}
	return evaluateWith(mf, rs.plan(mf), req, operators)
}

// plan is the rules of a manifest grouped by kind, each group sorted by priority (stable: equal
// priorities keep document order). Selecting from a sorted group gives the same order as sorting
// the selection, because a stable sort commutes with filtering. A plan remembers what it was built
// from, so that a manifest changed after load is planned again rather than evaluated stale.
type plan struct {
	manifest   *Object
	rules      []any     // the manifest's list of rules
	objects    []*Object // each rule, in document order
	kinds      []string  // the kind of each rule when the plan was built
	priorities []any     // the priority of each rule: a number, or nil for none
	byKind     map[string][]*Object
}

// planPriority is the priority a rule is ordered by: a number, or nil when it has none. Both are
// comparable with ==.
func planPriority(r *Object) any {
	if v := r.get("priority"); isNum(v) {
		return v
	}
	return nil
}

func newPlan(mf *Object) *plan {
	rules := mf.list("rules")
	p := &plan{manifest: mf, rules: rules, objects: make([]*Object, len(rules)), kinds: make([]string, len(rules)),
		priorities: make([]any, len(rules)), byKind: map[string][]*Object{}}
	type ranked struct {
		rule *Object
		p    decimal
	}
	groups := map[string][]ranked{}
	for i, x := range rules {
		if r := asObj(x); r != nil {
			p.objects[i], p.kinds[i], p.priorities[i] = r, r.str("kind"), planPriority(r)
			groups[p.kinds[i]] = append(groups[p.kinds[i]], ranked{r, dec(p.priorities[i])})
		}
	}
	for kind, g := range groups {
		sort.SliceStable(g, func(i, j int) bool { return g[i].p.cmp(g[j].p) > 0 })
		rules := make([]*Object, len(g))
		for i := range g {
			rules[i] = g[i].rule
		}
		p.byKind[kind] = rules
	}
	return p
}

// current reports whether the plan still describes mf: the same list of rules, and every rule the
// same object with the same kind and priority. Everything else a rule says is read when it runs.
func (p *plan) current(mf *Object) bool {
	if p == nil || p.manifest != mf {
		return false
	}
	rules := mf.list("rules")
	if len(rules) != len(p.rules) || (len(rules) > 0 && &rules[0] != &p.rules[0]) {
		return false
	}
	for i, x := range rules {
		r := asObj(x)
		if r != p.objects[i] || (r != nil && (r.str("kind") != p.kinds[i] || planPriority(r) != p.priorities[i])) {
			return false
		}
	}
	return true
}

func evaluate(mf *Object, req any, operators Operators) (*Result, error) {
	return evaluateWith(mf, nil, req, operators)
}

func evaluateWith(mf *Object, rulePlan *plan, req any, operators Operators) (*Result, error) {
	if why := requestProblem(req); why != "" {
		return nil, &RequestError{why}
	}
	channel, ok := mf.get("channel").(string)
	if !ok {
		return nil, errors.New("rulecascade: the manifest does not state its channel")
	}
	request := asObj(req)
	entity, operation := request.str("entity"), request.str("operation")
	data := request.obj("data").orEmpty() // working copy; compute rules write into it
	actor, view := request.obj("actor"), request.obj("view")
	vars := map[string]any{"data": data, "original": request.get("original"), "actor": actor.orEmpty(),
		"ctx": request.obj("ctx").orEmpty(), "params": mf.obj("params").orEmpty()}
	env := &scope{vars: vars, functions: mf.obj("functions"), operators: operators}
	client := channel == "client"
	wanted := "server"
	var trigger any
	if client {
		wanted, trigger = "client", request.get("trigger")
	}
	fieldTypes := mf.obj("fieldTypes").obj(entity)

	contains := func(list []any, v any) bool {
		for _, x := range list {
			if equal(x, v) {
				return true
			}
		}
		return false
	}
	selected := func(r *Object) bool {
		target := r.obj("target")
		enforcement := orDefault(r, "enforcement", "both")
		triggers, hasTriggers := r.Get("triggers")
		if !hasTriggers {
			triggers = []any{"submit"}
		}
		list, _ := triggers.([]any)
		if !truthy(orDefault(r, "enabled", true)) ||
			orDefault(target, "entity", entity) != any(entity) ||
			!contains(r.list("operations"), operation) ||
			!(enforcement == "both" || enforcement == any(wanted)) ||
			!(trigger == nil || contains(list, trigger)) {
			return false
		}
		// a view narrows evaluation to one place in the UI; rules that name no place always apply
		for _, k := range viewKeys {
			if place := view.get(k); place != nil && target.get(k) != nil && !equal(place, target.get(k)) {
				return false
			}
		}
		return true
	}
	if !rulePlan.current(mf) {
		rulePlan = newPlan(mf)
	}

	// Catalogs are consulted from most to least specific: fr-CA, then fr, then the default locale.
	messages := mf.obj("messages")
	defaultLocale, _ := orDefault(mf, "defaultLocale", "en").(string)
	// a tag longer than every catalog's tag cannot name one: a long locale costs no more than a short one
	wantedLocale := request.str("locale")
	longest := 0
	if wantedLocale != "" {
		longest = longestTag(messages)
	}
	locales := localeChain(defaultLocale, wantedLocale, longest)
	template := func(key string) string {
		for i := len(locales) - 1; i >= 0; i-- {
			if t, ok := messages.obj(locales[i]).get(key).(string); ok {
				return t
			}
		}
		return key
	}
	type resolutionKey struct{ rule, kind string }
	resolutions := map[resolutionKey]*Object{}
	for _, x := range request.list("resolutions") {
		r := asObj(x)
		resolutions[resolutionKey{r.str("rule"), r.str("type")}] = r
	}
	roles := map[string]bool{}
	for _, role := range actor.list("roles") {
		roles[role.(string)] = true
	}

	result := &Result{Ruleset: mf.str("id"), Version: mf.str("version"), Checksum: mf.str("checksum"),
		Findings: []Finding{}, Effects: []Effect{}, Commands: []Command{}}
	engineError := func(rule *Object, err *EvalError) {
		result.Findings = append(result.Findings, Finding{Rule: rule.str("id"), Code: EngineErrorCode, Severity: "error",
			Message: EngineErrorMessage, Fields: []string{}, Blocking: true, Status: "open", Resolution: "none",
			Source: rule.str("origin"), Detail: err.Message})
	}
	applies := func(rule *Object, s *scope) bool {
		when, ok := rule.Get("when")
		return !ok || boolean(ev(when, s))
	}
	priority := func(rule *Object) decimal {
		if p := rule.get("priority"); isNum(p) {
			return dec(p)
		}
		return decimalZero
	}
	ofKind := func(kind string) []*Object { // by priority, then document order
		var out []*Object
		for _, r := range rulePlan.byKind[kind] {
			if selected(r) {
				out = append(out, r)
			}
		}
		return out
	}
	// conflict is called when a second rule sets the same thing to a different value. It returns
	// when this rule loses quietly and raises when it is a real conflict.
	conflict := func(previous, rule *Object, what string) {
		if orDefault(mf, "conflictPolicy", "fail") == "fail" || priority(previous).cmp(priority(rule)) == 0 {
			fail("conflicting %s from %s", what, previous.str("id"))
		}
	}

	// Phase 1: compute
	type write struct {
		value any
		rule  *Object
	}
	written := map[string]write{}
	for _, r := range ofKind("compute") {
		if err := try(func() {
			if !applies(r, env) {
				return
			}
			for _, x := range r.list("assign") {
				a := asObj(x)
				field := a.str("field")
				if orDefault(a, "mode", "default") == "default" && lookup(pointerPath("data", field), vars) != nil {
					continue
				}
				value := plain(ev(a.get("value"), env))
				if previous, ok := written[field]; ok {
					if !equal(previous.value, value) {
						conflict(previous.rule, r, field)
					}
					continue
				}
				written[field] = write{value, r}
				setPointer(data, field, value)
				result.Effects = append(result.Effects, Effect{Type: "value", Field: field, Value: value, Rule: r.str("id")})
			}
		}); err != nil {
			engineError(r, err)
		}
	}

	// Phase 2: state. One effect per field and rule, in the order first set.
	type stateKey struct{ field, property string }
	type group struct{ field, rule string }
	state := map[stateKey]write{}
	groups := map[group]*Object{}
	var order []group
	for _, r := range ofKind("state") {
		if err := try(func() {
			if !applies(r, env) {
				return
			}
			// A rule applies all of its effects or, when one of them is a conflict, none of them:
			// the outcome never depends on the order of the members of `set`.
			staged := map[stateKey]write{}
			var keys []stateKey
			for _, x := range r.list("effects") {
				e := asObj(x)
				for _, property := range e.obj("set").names() {
					key, value := stateKey{e.str("field"), property}, e.obj("set").get(property)
					previous, set := staged[key]
					if !set {
						previous, set = state[key]
					}
					if set {
						if !equal(previous.value, value) {
							conflict(previous.rule, r, property+" of "+key.field)
						}
						continue
					}
					staged[key] = write{value, r}
					keys = append(keys, key)
				}
			}
			for _, key := range keys {
				state[key] = staged[key]
				g := group{key.field, r.str("id")}
				if groups[g] == nil {
					groups[g] = NewObject()
					order = append(order, g)
				}
				groups[g].Set(key.property, staged[key].value)
			}
		}); err != nil {
			engineError(r, err)
		}
	}
	for _, g := range order {
		result.Effects = append(result.Effects, Effect{Type: "state", Field: g.field, Set: groups[g], Rule: g.rule})
	}

	// Phase 3: validation
	type pass struct { // one evaluation of a validation rule
		scope  *scope
		fields []string
	}
	for _, r := range ofKind("validation") {
		if err := try(func() {
			target := r.obj("target")
			own := []string{}
			if field, ok := target.get("field").(string); ok {
				own = append(own, field)
			} else {
				for _, field := range target.list("fields") {
					own = append(own, fmt.Sprint(field))
				}
			}
			var passes []pass
			if typeName, ok := target.Get("type"); ok {
				// once per field bound to the type, in pointer order, with `value` and `field` in scope
				var pointers []string
				fieldTypes.each(func(pointer string, bound any) {
					if equal(bound, typeName) {
						pointers = append(pointers, pointer)
					}
				})
				sort.Strings(pointers)
				for _, p := range pointers {
					s := env.with("value", lookup(pointerPath("data", p), vars))
					s.vars["field"] = p
					passes = append(passes, pass{s, []string{p}})
				}
			} else if forEach, ok := r.get("forEach").(string); ok {
				var list []any
				switch x := lookup(pointerPath("data", forEach), vars).(type) {
				case nil:
				case []any:
					list = x
				default:
					fail("forEach %s is not a list", forEach)
				}
				for i, item := range list {
					fields := make([]string, len(own))
					for k, p := range own {
						fields[k] = fmt.Sprintf("%s/%d%s", forEach, i, p)
					}
					passes = append(passes, pass{env.with("item", item), fields})
				}
			} else {
				passes = []pass{{env, own}}
			}
			for _, p := range passes {
				if !applies(r, p.scope) || boolean(ev(r.get("assert"), p.scope)) {
					continue
				}
				spec := r.obj("finding")
				args := NewObject()
				spec.obj("args").each(func(name string, e any) { args.Set(name, plain(ev(e, p.scope))) })
				acceptance := r.obj("acceptance")
				acceptableBy := []string{}
				for _, role := range acceptance.list("roles") {
					acceptableBy = append(acceptableBy, fmt.Sprint(role))
				}
				severity := r.str("severity")
				status, resolution := "open", "none"
				if severity == "error" && truthy(acceptance.get("allowed")) {
					resolution = "accept-risk"
					given := resolutions[resolutionKey{r.str("id"), "accept-risk"}]
					roleOK := len(acceptableBy) == 0
					for _, role := range acceptableBy {
						roleOK = roleOK || roles[role]
					}
					justified := orDefault(acceptance, "justification", "required") == "none" || truthy(given.get("justification"))
					if given != nil && roleOK && justified {
						status = "accepted"
					}
				} else if severity == "warning" && r.str("acknowledgement") == "required" {
					resolution = "acknowledge"
					if resolutions[resolutionKey{r.str("id"), "acknowledge"}] != nil {
						status = "acknowledged"
					}
				}
				finding := Finding{
					Rule: r.str("id"), Code: spec.str("code"), Severity: severity,
					Message:  fill(template(spec.str("message")), args),
					Fields:   p.fields,
					Blocking: status == "open" && (severity == "error" || resolution == "acknowledge"),
					Status:   status, Resolution: resolution, Source: r.str("origin"),
					AcceptableBy: acceptableBy,
				}
				if place := (Place{target.str("page"), target.str("screen"), target.str("section"), target.str("component")}); place != (Place{}) {
					finding.Location = &place
				}
				result.Findings = append(result.Findings, finding)
			}
		}); err != nil {
			engineError(r, err)
		}
	}

	result.Decision = "allow"
	for _, f := range result.Findings {
		if f.Blocking {
			result.Decision = "deny"
		}
	}

	// Phase 4: action - server only, and only when allowed. The host runs commands after it has persisted.
	if result.Decision == "allow" && !client {
		for _, r := range ofKind("action") {
			if err := try(func() {
				if !applies(r, env) {
					return
				}
				for _, x := range r.list("commands") {
					c := asObj(x)
					parts := []string{}
					for _, p := range c.list("idempotencyKey") {
						parts = append(parts, render(plain(ev(p, env))))
					}
					payload := NewObject()
					c.obj("payload").each(func(name string, e any) { payload.Set(name, plain(ev(e, env))) })
					ref, hasRef := c.get("ref").(string)
					result.Commands = append(result.Commands, Command{Name: c.str("name"), Type: c.str("type"), Rule: r.str("id"),
						IdempotencyKey: strings.Join(parts, ":"), Payload: payload, Ref: ref, hasRef: hasRef})
				}
			}); err != nil {
				engineError(r, err)
				result.Decision, result.Commands = "deny", []Command{}
				break
			}
		}
	}
	return result, nil
}

func (o *Object) orEmpty() *Object {
	if o == nil {
		return NewObject()
	}
	return o
}
