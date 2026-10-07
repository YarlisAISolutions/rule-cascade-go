// Package rulehttp enforces Rule Cascade rules in net/http services (and every router built on it:
// chi, gorilla/mux, the standard ServeMux) with one middleware per operation:
//
//	mux.Handle("POST /transfers", rulehttp.Enforce(rules, rulehttp.Options{
//		Entity: "Transfer", Operation: "create",
//		Actor:  func(r *http.Request) map[string]any { return actorFromToken(r) },
//	})(http.HandlerFunc(createTransfer)))
//
// A denied request is answered with 422 problem details (the same shape in every Rule Cascade
// runtime); an allowed one reaches the handler, which finds the evaluation with FromContext: apply
// its computed values and run its commands after saving.
package rulehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	rulecascade "rulescascade.com/go"
)

// Options say how to build the evaluation request from an HTTP request.
type Options struct {
	Entity, Operation string
	// Data is the entity after the change. Default: the JSON request body, which stays readable
	// for the handler.
	Data func(r *http.Request) (any, error)
	// Original is the stored entity, for update, delete and actions. Default: none.
	Original func(r *http.Request) (any, error)
	// Actor is who acts, from your authentication, never from the body: {"id": ..., "roles": [...]}.
	Actor func(r *http.Request) map[string]any
	// Resolutions are the acknowledgements and risk acceptances the client sends. Default: none.
	Resolutions func(r *http.Request) []any
	// Ctx is the context the rules read. Default: {"now": <server time>}.
	Ctx func(r *http.Request) map[string]any
	// Channel defaults to server.
	Channel   string
	Operators rulecascade.Operators
	// MaxBody limits the body read by the default Data, in bytes. Default 1 MiB.
	MaxBody int64
}

type contextKey struct{}

// FromContext returns the allowed evaluation of the request, or nil outside Enforce.
func FromContext(ctx context.Context) *rulecascade.Result {
	result, _ := ctx.Value(contextKey{}).(*rulecascade.Result)
	return result
}

// Enforce returns middleware that enforces the rules before the handler runs.
func Enforce(rules *rulecascade.RuleSet, o Options) func(http.Handler) http.Handler {
	return EnforceFrom(func() *rulecascade.RuleSet { return rules }, o)
}

// EnforceFrom is Enforce with rules that can change, such as a Holder's: EnforceFrom(holder.Get, o).
func EnforceFrom(rules func() *rulecascade.RuleSet, o Options) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			request, err := build(r, o)
			if err != nil {
				writeProblem(w, http.StatusBadRequest, map[string]any{"type": "about:blank", "title": "Unreadable request", "status": 400, "detail": err.Error()})
				return
			}
			channel := o.Channel
			if channel == "" {
				channel = "server"
			}
			result, err := rules().Enforce(request, channel, o.Operators)
			if WriteError(w, err) {
				return
			}
			if err != nil {
				http.Error(w, "rules could not be evaluated", http.StatusInternalServerError)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, result)))
		})
	}
}

// WriteError answers a *RuleViolation with 422 problem details and a *RequestError with 400, and
// reports whether it wrote a response. Use it in handlers that call RuleSet.Enforce themselves.
func WriteError(w http.ResponseWriter, err error) bool {
	var violation *rulecascade.RuleViolation
	var malformed *rulecascade.RequestError
	switch {
	case errors.As(err, &violation):
		writeProblem(w, http.StatusUnprocessableEntity, violation.Problem())
	case errors.As(err, &malformed):
		writeProblem(w, http.StatusBadRequest, map[string]any{"type": "about:blank", "title": "Malformed evaluation request", "status": 400, "detail": malformed.Message})
	default:
		return false
	}
	return true
}

func writeProblem(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func build(r *http.Request, o Options) (map[string]any, error) {
	read := o.Data
	if read == nil {
		read = func(r *http.Request) (any, error) { return jsonBody(r, o.MaxBody) }
	}
	data, err := read(r)
	if err != nil {
		return nil, err
	}
	request := map[string]any{"entity": o.Entity, "operation": o.Operation, "data": data}
	if o.Original != nil {
		original, err := o.Original(r)
		if err != nil {
			return nil, err
		}
		request["original"] = original
	}
	if o.Actor != nil {
		if actor := o.Actor(r); actor != nil {
			request["actor"] = actor
		}
	}
	if o.Resolutions != nil {
		request["resolutions"] = o.Resolutions(r)
	}
	if o.Ctx != nil {
		request["ctx"] = o.Ctx(r)
	} else {
		request["ctx"] = map[string]any{"now": time.Now().UTC().Format(time.RFC3339)}
	}
	return request, nil
}

// jsonBody reads the body as JSON with exact numbers, and puts it back for the handler.
func jsonBody(r *http.Request, limit int64) (any, error) {
	if r.Body == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 1 << 20
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	r.Body.Close()
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("request body too large")
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	return rulecascade.ParseJSON(data)
}
