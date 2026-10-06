package rulecascade_test

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	rulecascade "rulescascade.com/go"
)

// read parses one of the JSON files of the conformance suite.
func read(name string) any {
	data, err := os.ReadFile("../../conformance/" + name)
	if err != nil {
		log.Fatal(err)
	}
	value, err := rulecascade.ParseJSON(data)
	if err != nil {
		log.Fatal(err)
	}
	return value
}

// An evaluator reads a bundle that was compiled elsewhere and evaluates requests against it.
func Example() {
	bundle := read("bundles/acme.payments.transfer.bundle.json")

	rules, err := rulecascade.FromBundle(bundle) // bundle: the parsed JSON of a *.bundle.json file
	if err != nil {
		log.Fatal(err) // a *rulecascade.LoadError: do not start
	}

	result, err := rules.Evaluate(map[string]any{
		"entity":    "Transfer",
		"operation": "create",
		"data": map[string]any{
			"type": "international", "amount": 12000, "currency": "USD",
			"beneficiary": map[string]any{"name": "Ana", "country": "ES"},
		},
		"actor": map[string]any{"id": "u-1", "roles": []string{"teller"}},
	}, "server", nil)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(result.Decision)
	for _, f := range result.Findings {
		fmt.Println(f.Code, f.Severity, f.Fields, f.Message)
	}
	for _, e := range result.Effects {
		if e.Type == "value" {
			fmt.Println(e.Field, "=", e.Value)
		}
	}
	// Output:
	// deny
	// ORG-TRF-003 warning [/memo] Adding a memo makes this transfer easier to reconcile.
	// PAY-TRF-002 error [/beneficiary/swiftCode] A valid SWIFT/BIC code is required for international transfers.
	// PAY-TRF-003 warning [/amount /beneficiary/name] This is a large transfer to Ana. Please confirm the details.
	// /fee = 180
}

// A compiler loads source documents, checks them and produces the bundle.
func ExampleLoad() {
	registry := map[string]any{
		"acme.org.base":          read("fixtures/acme-org-base.ruleset.json"),
		"acme.payments.transfer": read("fixtures/payments-transfer.ruleset.json"),
	}
	schemas := map[string]any{"./payments.openapi.yaml": read("fixtures/payments.openapi.json")}
	loader := func(file string) any { return schemas[file] }

	document := registry["acme.payments.transfer"]

	rules, err := rulecascade.Load(document, registry, loader)
	var failure *rulecascade.LoadError
	if errors.As(err, &failure) {
		for _, p := range failure.Problems {
			fmt.Println(p.Code, p.Rule, p.Message)
		}
		return
	} else if err != nil {
		log.Fatal(err)
	}
	fmt.Println(rules.ID(), rules.Version(), rules.Checksum())

	bundle, err := rulecascade.Marshal(rules.Bundle()) // what an evaluator reads with FromBundle
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(len(bundle) > 0, strings.HasPrefix(string(bundle), `{"ruleCascadeBundle":"1.0.0"`))

	client, err := rules.Manifest("client") // what a browser may see
	if err != nil {
		log.Fatal(err)
	}
	operators, _ := client.Get("operators")
	fmt.Println(operators)
	// Output:
	// acme.payments.transfer 1.0.0 sha256:c192dd53b5b1d307d52ccbc27fc1674114e8714d53b699b24088a648ae242c7e
	// true true
	// []
}

// Custom operators are supplied by the host, in every runtime that evaluates the ruleset.
func ExampleOperators() {
	operators := rulecascade.Operators{
		// true for a string of digits whose Luhn check digit is valid
		"x-luhn": func(args []any) (any, error) {
			digits, ok := args[0].(string)
			if !ok || len(digits) < 2 {
				return false, nil
			}
			sum := 0
			for i := 0; i < len(digits); i++ {
				d := int(digits[len(digits)-1-i]) - '0'
				if d < 0 || d > 9 {
					return false, nil
				}
				if i%2 == 1 {
					if d *= 2; d > 9 {
						d -= 9
					}
				}
				sum += d
			}
			return sum%10 == 0, nil
		},
	}
	rules, err := rulecascade.FromBundle(read("bundles/acme.onboarding.customer.bundle.json"))
	if err != nil {
		log.Fatal(err)
	}
	request := map[string]any{
		"entity":    "Customer",
		"operation": "create",
		"data": map[string]any{
			"fullName": "Maya Okafor", "email": "maya@example.com", "accountType": "personal", "country": "US",
			"termsAccepted": true, "loyaltyNumber": "79927398710",
		},
	}

	result, err := rules.Evaluate(request, "server", operators)
	if err != nil {
		log.Fatal(err)
	}
	for _, f := range result.Findings {
		fmt.Println(f.Code, f.Message)
	}

	result, _ = rules.Evaluate(request, "server", nil) // not registered: the rule fails closed
	for _, f := range result.Findings {
		fmt.Println(f.Code, f.Blocking, f.Detail)
	}
	// Output:
	// ONB-CUS-001 This loyalty number is not valid. Check the digits.
	// RULE-EVALUATION-ERROR true custom operator x-luhn is not registered
}

// Arithmetic is decimal: 34 significant digits inside an expression, 15 when a number leaves it.
func ExampleEvaluateExpression() {
	sum, _ := rulecascade.EvaluateExpression(map[string]any{"op": "add", "args": []any{0.1, 0.2}}, nil, nil, nil)
	third, _ := rulecascade.EvaluateExpression(map[string]any{"op": "div", "args": []any{1, 3}}, nil, nil, nil)
	_, err := rulecascade.EvaluateExpression(map[string]any{"op": "div", "args": []any{1, 0}}, nil, nil, nil)
	fmt.Println(sum, third, err)
	// Output:
	// 0.3 0.333333333333333 division by zero
}

// A browser or a mobile app receives one manifest, not the bundle. The ruleset then has that one
// channel.
func ExampleFromManifest() {
	published, err := rulecascade.FromBundle(read("bundles/acme.onboarding.customer.bundle.json"))
	if err != nil {
		log.Fatal(err)
	}
	manifest, err := published.Manifest("client") // what a rule server sends to a client
	if err != nil {
		log.Fatal(err)
	}

	rules, err := rulecascade.FromManifest(manifest)
	if err != nil {
		log.Fatal(err) // a *rulecascade.LoadError with code MANIFEST_INVALID
	}
	fmt.Println(rules.Channels(), rules.MissingOperators(nil)) // check the operators at start-up

	request := map[string]any{
		"entity": "Customer", "operation": "create", "trigger": "blur", "locale": "es-MX",
		"data": map[string]any{"email": "maya(at)example.com"},
	}
	result, err := rules.Evaluate(request, "", nil) // "" is the channel the ruleset has
	if err != nil {
		log.Fatal(err)
	}
	for _, f := range result.Findings {
		fmt.Println(f.Code, f.Message)
	}

	_, err = rules.Evaluate(request, "server", nil)
	fmt.Println(err)
	// Output:
	// [client] [x-luhn]
	// ONB-STR-001 Introduce tu nombre completo.
	// ONB-TYP-001 Introduce una dirección de correo válida.
	// rulecascade: ruleset acme.onboarding.customer has no server manifest
}
