// Package jsonlogic implements a minimal JSONLogic evaluator for workflow
// edge conditions (P1-2, blueprint TS-8). Supports 11 operators covering
// equality / comparison / logic / membership / variable access. The evaluator
// NEVER panics — any malformed input or type mismatch returns false.
//
// Supported operators: == != > < >= <= and or not in var
//
// Evaluation context (data) is limited to three namespaces per blueprint TS-8:
//
//	{verdict: {...}, exit_fields: {...}, run: {context: {...}}}
//
// Top-level expression MUST be a JSON object with exactly one key (the operator).
// Literal / array / bare value top-level expressions are NOT supported
// (blueprint examples never use them).
//
// This is a self-contained subpackage: stdlib only, no third-party deps
// (PRD Q1 decision: 自研精简, multica "minimal deps" style).
//
// NOTE on operator count: PRD R1 says "10 operators" but enumerates 11
// (== != > < >= <= and or not in var). This package implements the 11
// actually enumerated. See SupportedOperators and the task report.
package jsonlogic

import (
	"encoding/json"
	"fmt"
	"strings"
)

// operatorSet is the supported-operator lookup table.
var operatorSet = map[string]struct{}{
	"==": {}, "!=": {},
	">": {}, "<": {}, ">=": {}, "<=": {},
	"and": {}, "or": {}, "not": {},
	"in": {}, "var": {},
}

// SupportedOperators returns the supported operator set, sorted for stable
// output (useful for error messages, documentation, and CLI surface).
//
// Returns 11 operators (PRD R1 miscounts as "10" but the enumerated set is 11).
func SupportedOperators() []string {
	return []string{
		"!=", "<", "<=", "==", ">", ">=",
		"and", "in", "not", "or", "var",
	}
}

// Evaluate returns true iff expr evaluates to truthy against data.
//
// Top-level expr MUST be a single-key map whose key is a known operator
// (design §3.1: "Top-level expression MUST be a JSON object with exactly
// one key (the operator)"). Empty maps, multi-key maps, and maps whose
// sole key is not a known operator all return false — they are not valid
// top-level JSONLogic expressions.
//
// No-panic contract: any panic during evaluation is recovered and turned
// into false (PRD R5: runtime evaluation failure → condition not matched,
// run proceeds to catch-all or blocked state).
func Evaluate(expr map[string]any, data any) (result bool) {
	defer func() {
		if r := recover(); r != nil {
			result = false
		}
	}()
	if len(expr) != 1 {
		return false
	}
	for op := range expr {
		if _, known := operatorSet[op]; !known {
			return false
		}
	}
	v := evalValue(expr, data)
	return truthy(v)
}

// ValidateSchema checks raw is a structurally valid JSONLogic expression:
//   - decodes to a JSON object with exactly one key (the operator),
//   - the operator is supported,
//   - argument count and shape match the operator's signature,
//   - any sub-expressions are recursively valid.
//
// Used at publish time to fail-fast (PRD R4: schema validation left-shift;
// design §2.1 validateEdgeConditions).
//
// No-panic contract: any panic (e.g. deeply nested input triggering stack
// exhaustion) is recovered and reported as an error.
func ValidateSchema(raw json.RawMessage) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("edge condition validation panicked: %v", r)
		}
	}()
	var node any
	if err = json.Unmarshal(raw, &node); err != nil {
		return fmt.Errorf("edge condition is not valid JSON: %w", err)
	}
	return validateNode(node)
}

// validateNode recursively validates an expression node.
func validateNode(node any) error {
	m, ok := node.(map[string]any)
	if !ok {
		return fmt.Errorf("edge condition must be a JSON object, got %T", node)
	}
	if len(m) != 1 {
		return fmt.Errorf("edge condition must have exactly one key, got %d", len(m))
	}
	for op, rawArgs := range m {
		if _, known := operatorSet[op]; !known {
			return fmt.Errorf("unknown operator: %q", op)
		}
		if err := validateArgs(op, rawArgs); err != nil {
			return err
		}
		// var's args are literal path + default; not sub-expressions, so do
		// not recurse into them.
		if op == "var" {
			continue
		}
		arr, _ := rawArgs.([]any)
		for _, sub := range arr {
			// Only recurse into single-key maps whose key is a known operator
			// (i.e. actual sub-expressions; literal maps stay literal).
			if subMap, ok := sub.(map[string]any); ok && len(subMap) == 1 {
				for subOp := range subMap {
					if _, known := operatorSet[subOp]; known {
						if err := validateNode(sub); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

// validateArgs validates the shape and count of args for op.
func validateArgs(op string, raw any) error {
	// var accepts string form (path only) or array form ([path] or [path, default]).
	if op == "var" {
		switch a := raw.(type) {
		case string:
			return nil
		case []any:
			if len(a) < 1 || len(a) > 2 {
				return fmt.Errorf("operator %q expects 1-2 args, got %d", op, len(a))
			}
			if _, ok := a[0].(string); !ok {
				return fmt.Errorf("operator %q expects arg[0] to be a string path, got %T", op, a[0])
			}
			return nil
		default:
			return fmt.Errorf("operator %q expects string or array, got %T", op, raw)
		}
	}
	// All other operators require array form.
	arr, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("operator %q expects array args, got %T", op, raw)
	}
	switch op {
	case "==", "!=", ">", "<", ">=", "<=", "in":
		if len(arr) != 2 {
			return fmt.Errorf("operator %q expects 2 args, got %d", op, len(arr))
		}
	case "and", "or":
		if len(arr) < 1 {
			return fmt.Errorf("operator %q expects ≥1 args, got %d", op, len(arr))
		}
	case "not":
		if len(arr) != 1 {
			return fmt.Errorf("operator %q expects 1 arg, got %d", op, len(arr))
		}
	}
	return nil
}

// evalValue resolves expr to a JSON value, recursively evaluating any nested
// JSONLogic expressions. Non-expression values (literals) are returned as-is.
//
// An expr is treated as a sub-expression iff it is a single-key map whose
// key is a known operator. All other values are literals (including maps
// whose key is not a known operator).
func evalValue(expr any, data any) any {
	m, ok := expr.(map[string]any)
	if !ok || len(m) != 1 {
		return expr
	}
	for op, args := range m {
		if _, known := operatorSet[op]; !known {
			return expr // literal map (not an expression)
		}
		return evalOperator(op, args, data)
	}
	return expr
}

// evalOperator dispatches to the per-operator implementation. Returns any
// JSON value (bool for comparison/logic ops; arbitrary value for var).
func evalOperator(op string, raw any, data any) any {
	if op == "var" {
		return evalVar(raw, data)
	}
	arr, ok := raw.([]any)
	if !ok {
		// Non-array args for non-var operators: malformed → false at runtime.
		// (Schema validation rejects this at publish time.)
		return false
	}
	switch op {
	case "==", "!=":
		if len(arr) != 2 {
			return false
		}
		eq := equalValues(evalValue(arr[0], data), evalValue(arr[1], data))
		if op == "!=" {
			return !eq
		}
		return eq
	case ">", "<", ">=", "<=":
		return evalComparison(op, arr, data)
	case "and":
		for _, sub := range arr {
			if !truthy(evalValue(sub, data)) {
				return false
			}
		}
		return true
	case "or":
		for _, sub := range arr {
			if truthy(evalValue(sub, data)) {
				return true
			}
		}
		return false
	case "not":
		if len(arr) != 1 {
			return false
		}
		return !truthy(evalValue(arr[0], data))
	case "in":
		if len(arr) != 2 {
			return false
		}
		return contains(evalValue(arr[0], data), evalValue(arr[1], data))
	}
	return false
}

// evalComparison handles > < >= <=. Returns false on any type/arity mismatch.
func evalComparison(op string, arr []any, data any) bool {
	if len(arr) != 2 {
		return false
	}
	c, ok := compareNum(evalValue(arr[0], data), evalValue(arr[1], data))
	if !ok {
		return false
	}
	switch op {
	case ">":
		return c > 0
	case "<":
		return c < 0
	case ">=":
		return c >= 0
	case "<=":
		return c <= 0
	}
	return false // unreachable; satisfies compiler
}

// evalVar resolves {"var": "path"} / {"var": ["path"]} / {"var": ["path", default]}.
// Returns nil (or the default) if the path is missing or malformed.
func evalVar(raw any, data any) any {
	var path string
	var def any
	hasDef := false
	switch a := raw.(type) {
	case string:
		path = a
	case []any:
		if len(a) < 1 || len(a) > 2 {
			return nil
		}
		p, ok := a[0].(string)
		if !ok {
			return nil
		}
		path = p
		if len(a) == 2 {
			def = a[1]
			hasDef = true
		}
	default:
		return nil
	}
	v := resolvePath(data, path)
	if v == nil && hasDef {
		return def
	}
	return v
}

// resolvePath traverses data by dotted path with array index support.
// Returns nil if any segment is missing or type-mismatched (no panic).
//
// Examples:
//   - "verdict.result"          → data["verdict"]["result"]
//   - "verdict.evidence.0.type" → data["verdict"]["evidence"][0]["type"]
//   - ""                         → data itself
func resolvePath(data any, path string) any {
	if path == "" {
		return data
	}
	cur := data
	for _, seg := range strings.Split(path, ".") {
		cur = stepPath(cur, seg)
		if cur == nil {
			return nil
		}
	}
	return cur
}

// stepPath advances one path segment into cur. Returns nil on any mismatch.
func stepPath(cur any, seg string) any {
	if m, ok := cur.(map[string]any); ok {
		v, exists := m[seg]
		if !exists {
			return nil
		}
		return v
	}
	if arr, ok := cur.([]any); ok {
		idx := parseIndex(seg)
		if idx < 0 || idx >= len(arr) {
			return nil
		}
		return arr[idx]
	}
	return nil
}

// parseIndex parses a non-negative decimal integer. Returns -1 on failure
// (non-digit, empty, or overflow). Used for array subscript resolution.
func parseIndex(s string) int {
	if s == "" {
		return -1
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
		// Overflow guard (reject absurdly large indices without wrapping).
		if n > 1<<30 {
			return -1
		}
	}
	return n
}

// truthy implements JSONLogic truthiness:
//
//	false / 0 / "" / null / [] / {} → false
//	everything else                 → true
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// equalValues compares two JSON values by canonical serialization.
// Handles number/int boundary (JSON unifies to float64) and distinguishes
// 1 vs "1" vs true (which all marshal differently).
func equalValues(a, b any) bool {
	aj, errA := json.Marshal(a)
	bj, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(aj) == string(bj)
}

// compareNum compares two values as floats. Returns (-1|0|1, true) when both
// are numeric; (0, false) when either is non-numeric (caller treats as
// "not comparable" and returns false).
func compareNum(a, b any) (int, bool) {
	af, okA := toFloat(a)
	bf, okB := toFloat(b)
	if !okA || !okB {
		return 0, false
	}
	if af < bf {
		return -1, true
	}
	if af > bf {
		return 1, true
	}
	return 0, true
}

// toFloat converts numeric JSON values (and Go-native int/int64) to float64.
// JSON unmarshals all numbers as float64, but tests/literals may use int.
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

// contains implements the `in` operator:
//   - string haystack → substring match (needle must be string)
//   - array haystack   → element match (deep equal via JSON marshal)
//   - object haystack  → key match (needle must be string)
//
// Returns false for any other haystack type or needle/haystack type mismatch.
func contains(needle, haystack any) bool {
	switch h := haystack.(type) {
	case string:
		n, ok := needle.(string)
		if !ok {
			return false
		}
		return strings.Contains(h, n)
	case []any:
		for _, item := range h {
			if equalValues(needle, item) {
				return true
			}
		}
		return false
	case map[string]any:
		n, ok := needle.(string)
		if !ok {
			return false
		}
		_, exists := h[n]
		return exists
	}
	return false
}
