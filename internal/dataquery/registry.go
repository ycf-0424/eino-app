// Package dataquery provides an allowlisted, read-only registry for future
// internal business data sources. User/model input is never interpreted as SQL
// or as a network destination.
package dataquery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrDisabled       = errors.New("no realtime data source is configured")
	ErrUnknownSource  = errors.New("unregistered data source")
	ErrUnknownOp      = errors.New("unregistered query operation")
	ErrInvalidRequest = errors.New("invalid structured query")
	ErrForeignOwner   = errors.New("owner is not authorized for this data source")
	ErrStaleResult    = errors.New("query result is stale")
)

type ValueKind string

const (
	StringValue  ValueKind = "string"
	IntegerValue ValueKind = "integer"
	NumberValue  ValueKind = "number"
	BooleanValue ValueKind = "boolean"
)

type Parameter struct {
	Kind     ValueKind
	Required bool
	MaxBytes int
	Enum     []string
}

// QueryRequest is server-created after validating source, operation,
// arguments, and owner. Handler implementations must use OwnerID as a tenant
// filter and must not accept SQL, URLs, or credentials from Arguments.
type QueryRequest struct {
	Source    string
	Operation string
	Arguments map[string]any
	OwnerID   string
}

type QueryResult struct {
	Rows       []map[string]any `json:"rows"`
	Source     string           `json:"source"`
	Operation  string           `json:"operation"`
	AsOf       time.Time        `json:"as_of"`
	FreshUntil time.Time        `json:"fresh_until"`
	Redactions []string         `json:"redactions,omitempty"`
}

type Handler func(context.Context, QueryRequest) (QueryResult, error)

type Operation struct {
	Source         string
	ID             string
	Parameters     map[string]Parameter
	AllowedFields  []string
	RedactedFields []string
	OwnerRequired  bool
	ReadOnly       bool
	AllowBatch     bool
	MaxRows        int
	MaxBytes       int
	Timeout        time.Duration
	FreshnessTTL   time.Duration
	Handler        Handler
}

type Registry struct {
	mu      sync.RWMutex
	ops     map[string]Operation
	sources map[string]struct{}
}

func NewRegistry() *Registry {
	return &Registry{ops: make(map[string]Operation), sources: make(map[string]struct{})}
}

var identifierRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,63}$`)

func (r *Registry) Register(op Operation) error {
	if r == nil {
		return ErrDisabled
	}
	op.Source = strings.TrimSpace(op.Source)
	op.ID = strings.TrimSpace(op.ID)
	if !identifierRE.MatchString(op.Source) || !identifierRE.MatchString(op.ID) {
		return fmt.Errorf("%w: source and operation IDs must be registered identifiers", ErrInvalidRequest)
	}
	if !op.ReadOnly {
		return fmt.Errorf("%w: only read-only operations may be registered", ErrInvalidRequest)
	}
	if op.Handler == nil || op.MaxRows < 1 || op.MaxRows > 1000 || op.MaxBytes < 1 || op.MaxBytes > 1<<20 || op.Timeout <= 0 || op.Timeout > 30*time.Second || op.FreshnessTTL <= 0 {
		return fmt.Errorf("%w: handler, bounded limits, timeout, and freshness TTL are required", ErrInvalidRequest)
	}
	if len(op.AllowedFields) == 0 {
		return fmt.Errorf("%w: at least one return field must be allowlisted", ErrInvalidRequest)
	}
	for name := range op.Parameters {
		if !identifierRE.MatchString(name) || unsafeParameter(name) {
			return fmt.Errorf("%w: invalid parameter name", ErrInvalidRequest)
		}
		parameter := op.Parameters[name]
		switch parameter.Kind {
		case StringValue:
			if parameter.MaxBytes < 1 || parameter.MaxBytes > 4096 {
				return fmt.Errorf("%w: string parameters require a 1..4096 byte limit", ErrInvalidRequest)
			}
		case IntegerValue, NumberValue, BooleanValue:
		default:
			return fmt.Errorf("%w: unsupported parameter type", ErrInvalidRequest)
		}
	}
	for _, name := range append(append([]string(nil), op.AllowedFields...), op.RedactedFields...) {
		if !identifierRE.MatchString(name) || sensitiveField(name) {
			return fmt.Errorf("%w: unsafe result field", ErrInvalidRequest)
		}
	}
	op.Parameters = cloneParameters(op.Parameters)
	op.AllowedFields = uniqueStrings(op.AllowedFields)
	op.RedactedFields = uniqueStrings(op.RedactedFields)
	key := op.Source + "." + op.ID
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.ops[key]; exists {
		return fmt.Errorf("%w: operation already registered", ErrInvalidRequest)
	}
	r.ops[key] = op
	r.sources[op.Source] = struct{}{}
	return nil
}

func (r *Registry) Enabled() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.ops) > 0
}

func (r *Registry) Execute(ctx context.Context, req QueryRequest) (QueryResult, error) {
	if r == nil || !r.Enabled() {
		return QueryResult{}, ErrDisabled
	}
	key := strings.TrimSpace(req.Source) + "." + strings.TrimSpace(req.Operation)
	r.mu.RLock()
	op, ok := r.ops[key]
	r.mu.RUnlock()
	if !ok {
		r.mu.RLock()
		_, knownSource := r.sources[strings.TrimSpace(req.Source)]
		r.mu.RUnlock()
		if strings.TrimSpace(req.Source) == "" || !knownSource {
			return QueryResult{}, ErrUnknownSource
		}
		return QueryResult{}, ErrUnknownOp
	}
	if op.OwnerRequired && strings.TrimSpace(req.OwnerID) == "" {
		return QueryResult{}, ErrForeignOwner
	}
	arguments, err := validateArguments(op.Parameters, req.Arguments)
	if err != nil {
		return QueryResult{}, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, op.Timeout)
	defer cancel()
	type handlerResult struct {
		result QueryResult
		err    error
	}
	completed := make(chan handlerResult, 1)
	request := QueryRequest{Source: op.Source, Operation: op.ID, Arguments: arguments, OwnerID: req.OwnerID}
	go func() {
		result, err := op.Handler(queryCtx, request)
		completed <- handlerResult{result: result, err: err}
	}()
	var result QueryResult
	select {
	case <-queryCtx.Done():
		return QueryResult{}, queryCtx.Err()
	case response := <-completed:
		result, err = response.result, response.err
	}
	if err != nil {
		return QueryResult{}, err
	}
	if len(result.Rows) > op.MaxRows {
		return QueryResult{}, fmt.Errorf("%w: row limit exceeded", ErrInvalidRequest)
	}
	if result.AsOf.IsZero() {
		return QueryResult{}, fmt.Errorf("%w: source did not provide as_of", ErrInvalidRequest)
	}
	deadline := result.AsOf.Add(op.FreshnessTTL)
	if result.FreshUntil.IsZero() || deadline.Before(result.FreshUntil) {
		result.FreshUntil = deadline
	}
	if result.FreshUntil.Before(result.AsOf) {
		return QueryResult{}, fmt.Errorf("%w: invalid freshness window", ErrInvalidRequest)
	}
	if time.Now().After(result.FreshUntil) {
		return QueryResult{}, ErrStaleResult
	}
	result.Rows = filterRows(result.Rows, op.AllowedFields)
	result.Source, result.Operation = op.Source, op.ID
	result.Redactions = uniqueStrings(op.RedactedFields)
	encoded, err := json.Marshal(result.Rows)
	if err != nil || len(encoded) > op.MaxBytes {
		return QueryResult{}, fmt.Errorf("%w: response size limit exceeded", ErrInvalidRequest)
	}
	return result, nil
}

func unsafeParameter(name string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "_", ""), "-", ""))
	return normalized == "sql" || normalized == "query" || normalized == "url" || normalized == "uri" ||
		normalized == "owner" || normalized == "ownerid" || normalized == "tenant" || normalized == "tenantid" || sensitiveField(normalized)
}

func validateArguments(spec map[string]Parameter, args map[string]any) (map[string]any, error) {
	if len(args) > len(spec) {
		return nil, fmt.Errorf("%w: unknown argument", ErrInvalidRequest)
	}
	clean := make(map[string]any, len(args))
	for key := range args {
		if _, ok := spec[key]; !ok {
			return nil, fmt.Errorf("%w: unknown argument", ErrInvalidRequest)
		}
	}
	for name, param := range spec {
		value, present := args[name]
		if !present {
			if param.Required {
				return nil, fmt.Errorf("%w: required argument missing", ErrInvalidRequest)
			}
			continue
		}
		switch param.Kind {
		case StringValue:
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) == "" || (param.MaxBytes > 0 && len(text) > param.MaxBytes) {
				return nil, fmt.Errorf("%w: invalid string argument", ErrInvalidRequest)
			}
			if len(param.Enum) > 0 && !contains(param.Enum, text) {
				return nil, fmt.Errorf("%w: argument value is not allowed", ErrInvalidRequest)
			}
			clean[name] = text
		case IntegerValue:
			n, ok := integer(value)
			if !ok {
				return nil, fmt.Errorf("%w: invalid integer argument", ErrInvalidRequest)
			}
			clean[name] = n
		case NumberValue:
			n, ok := number(value)
			if !ok {
				return nil, fmt.Errorf("%w: invalid numeric argument", ErrInvalidRequest)
			}
			clean[name] = n
		case BooleanValue:
			b, ok := value.(bool)
			if !ok {
				return nil, fmt.Errorf("%w: invalid boolean argument", ErrInvalidRequest)
			}
			clean[name] = b
		default:
			return nil, fmt.Errorf("%w: unsupported parameter type", ErrInvalidRequest)
		}
	}
	return clean, nil
}

func integer(value any) (int64, bool) {
	switch n := value.(type) {
	case json.Number:
		parsed, err := n.Int64()
		return parsed, err == nil
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true
		}
	}
	return 0, false
}

func number(value any) (float64, bool) {
	switch n := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(n.String(), 64)
		return parsed, err == nil
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

func filterRows(rows []map[string]any, allowed []string) []map[string]any {
	set := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		set[name] = struct{}{}
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		filtered := make(map[string]any)
		for key, value := range row {
			if _, ok := set[key]; ok && !sensitiveField(key) {
				filtered[key] = value
			}
		}
		out = append(out, filtered)
	}
	return out
}

func sensitiveField(name string) bool {
	name = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "_", ""), "-", ""))
	return strings.Contains(name, "password") || strings.Contains(name, "secret") || strings.Contains(name, "token") || strings.Contains(name, "apikey")
}

func cloneParameters(in map[string]Parameter) map[string]Parameter {
	out := make(map[string]Parameter, len(in))
	for name, param := range in {
		param.Enum = append([]string(nil), param.Enum...)
		out[name] = param
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
