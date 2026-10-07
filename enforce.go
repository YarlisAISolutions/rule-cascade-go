package rulecascade

import (
	"fmt"
	"strings"
)

// RuleViolationType is the problem type of a denied operation (RFC 9457).
const RuleViolationType = "urn:rule-cascade:rule-violation"

// ViolationProblem is the answer to a denied operation: RFC 9457 problem details with the whole evaluation,
// sent with status 422 and Content-Type application/problem+json. The same shape in every runtime.
type ViolationProblem struct {
	Type       string  `json:"type"`
	Title      string  `json:"title"`
	Status     int     `json:"status"`
	Detail     string  `json:"detail"`
	Evaluation *Result `json:"evaluation"`
}

// ProblemDetails returns the problem details of a denied evaluation.
func ProblemDetails(result *Result) ViolationProblem {
	var blocking []string
	for _, f := range result.Findings {
		if f.Blocking {
			blocking = append(blocking, f.Code)
		}
	}
	noun := "findings"
	if len(blocking) == 1 {
		noun = "finding"
	}
	detail := fmt.Sprintf("Denied by %d blocking %s", len(blocking), noun)
	if len(blocking) > 0 {
		detail += ": " + strings.Join(blocking, ", ")
	}
	return ViolationProblem{Type: RuleViolationType, Title: "Business rule violation", Status: 422, Detail: detail + ".", Evaluation: result}
}

// RuleViolation is the error for an operation the rules deny. Result is the evaluation; Problem()
// is the HTTP answer.
type RuleViolation struct{ Result *Result }

func (e *RuleViolation) Error() string { return ProblemDetails(e.Result).Detail }

// Problem returns the problem details to send with status 422.
func (e *RuleViolation) Problem() ViolationProblem { return ProblemDetails(e.Result) }

// Enforce evaluates a request like Evaluate and returns the result when it is allowed. A denied
// request returns the result and a *RuleViolation (answer 422 with its Problem); a malformed one a
// *RequestError (answer 400).
func (rs *RuleSet) Enforce(request any, channel string, operators Operators) (*Result, error) {
	result, err := rs.Evaluate(request, channel, operators)
	if err != nil {
		return nil, err
	}
	if !result.Allowed() {
		return result, &RuleViolation{Result: result}
	}
	return result, nil
}
