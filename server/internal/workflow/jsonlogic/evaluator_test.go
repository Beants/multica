package jsonlogic

import (
	"encoding/json"
	"testing"
)

// mk builds {op: [a, b, ...]} — the canonical JSONLogic array form.
func mk(op string, args ...any) map[string]any {
	return map[string]any{op: args}
}

// singleArr builds {op: [a]} — the 1-arg array form.
func singleArr(op string, a any) map[string]any {
	return map[string]any{op: []any{a}}
}

func TestEvaluate_Equality(t *testing.T) {
	cases := []struct {
		name string
		expr map[string]any
		want bool
	}{
		// string
		{"string equal", mk("==", "hello", "hello"), true},
		{"string not equal", mk("==", "hello", "world"), false},
		// number (JSON unify: all numbers → float64)
		{"number equal", mk("==", 1.0, 1.0), true},
		{"number not equal", mk("==", 1.5, 2.5), false},
		{"int vs float unify", mk("==", 1, 1.0), true},
		// bool
		{"bool true", mk("==", true, true), true},
		{"bool false", mk("==", true, false), false},
		// null
		{"null equal", mk("==", nil, nil), true},
		{"null vs string", mk("==", nil, "x"), false},
		// != negation
		{"!= negation true", mk("!=", "a", "b"), true},
		{"!= negation false", mk("!=", "a", "a"), false},
		// number vs string boundary
		{"number vs string distinct", mk("==", 1.0, "1"), false},
		{"number vs bool distinct", mk("==", 1.0, true), false},
		// container equality
		{"array equal", mk("==", []any{1.0, 2.0}, []any{1.0, 2.0}), true},
		{"array order matters", mk("==", []any{1.0, 2.0}, []any{2.0, 1.0}), false},
		{"object equal", mk("==", map[string]any{"a": 1.0}, map[string]any{"a": 1.0}), true},
		{"object key count differs", mk("==", map[string]any{"a": 1.0}, map[string]any{"a": 1.0, "b": 2.0}), false},
		// wrong arg count
		{"== one arg", singleArr("==", 1.0), false},
		{"== three args", mk("==", 1.0, 2.0, 3.0), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Evaluate(c.expr, nil); got != c.want {
				t.Errorf("Evaluate(%v) = %v; want %v", c.expr, got, c.want)
			}
		})
	}
}

func TestEvaluate_Comparison(t *testing.T) {
	cases := []struct {
		name string
		expr map[string]any
		want bool
	}{
		// numeric
		{"gt true", mk(">", 2.0, 1.0), true},
		{"gt false", mk(">", 1.0, 2.0), false},
		{"lt true", mk("<", 1.0, 2.0), true},
		{"lt false", mk("<", 2.0, 1.0), false},
		{"ge gt", mk(">=", 2.0, 1.0), true},
		{"ge eq", mk(">=", 1.0, 1.0), true},
		{"ge lt", mk(">=", 1.0, 2.0), false},
		{"le lt", mk("<=", 1.0, 2.0), true},
		{"le eq", mk("<=", 1.0, 1.0), true},
		{"le gt", mk("<=", 2.0, 1.0), false},
		{"negative numbers", mk("<", -2.0, -1.0), true},
		// type mismatch → false
		{"gt strings", mk(">", "a", "b"), false},
		{"gt mixed types", mk(">", 1.0, "2"), false},
		{"gt null", mk(">", nil, 1.0), false},
		{"gt bool", mk(">", true, 1.0), false},
		// wrong arg count → false
		{"gt one arg", singleArr(">", 1.0), false},
		{"gt three args", mk(">", 1.0, 2.0, 3.0), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Evaluate(c.expr, nil); got != c.want {
				t.Errorf("Evaluate(%v) = %v; want %v", c.expr, got, c.want)
			}
		})
	}
}

func TestEvaluate_Logic(t *testing.T) {
	// truthy definition matrix: false/0/""/null/[]/{} → false; else true.
	// We verify each by wrapping in `not` and checking the negation.
	truthyCases := []struct {
		name  string
		value any
		falsy bool // true if expected falsy
	}{
		{"false is falsy", false, true},
		{"true is truthy", true, false},
		{"zero float is falsy", 0.0, true},
		{"non-zero float is truthy", 1.5, false},
		{"negative is truthy", -1.0, false},
		{"zero int is falsy", 0, true},
		{"non-zero int is truthy", 42, false},
		{"empty string is falsy", "", true},
		{"non-empty string is truthy", "x", false},
		{"null is falsy", nil, true},
		{"empty array is falsy", []any{}, true},
		{"non-empty array is truthy", []any{1.0}, false},
		{"empty object is falsy", map[string]any{}, true},
		{"non-empty object is truthy", map[string]any{"a": 1.0}, false},
	}
	for _, c := range truthyCases {
		t.Run(c.name, func(t *testing.T) {
			expr := map[string]any{"not": []any{c.value}}
			got := Evaluate(expr, nil)
			want := c.falsy // not(falsy) = true
			if got != want {
				t.Errorf("not(%v): got %v; want %v (truthy=%v)", c.value, got, want, !c.falsy)
			}
		})
	}

	// and
	if !Evaluate(mk("and", true, true, true), nil) {
		t.Error("and: all truthy → true")
	}
	if Evaluate(mk("and", true, false, true), nil) {
		t.Error("and: any falsy → false")
	}
	if !Evaluate(singleArr("and", true), nil) {
		t.Error("and: single truthy → true")
	}
	// or
	if !Evaluate(mk("or", false, true, false), nil) {
		t.Error("or: any truthy → true")
	}
	if Evaluate(mk("or", false, false), nil) {
		t.Error("or: all falsy → false")
	}
	// not arg-count enforcement
	if Evaluate(mk("not", true, false), nil) {
		t.Error("not: wrong arg count → false")
	}
	if Evaluate(singleArr("not", nil), nil) == false {
		t.Error("not: nil should be truthy-negated to true")
	}
}

func TestEvaluate_In(t *testing.T) {
	cases := []struct {
		name string
		expr map[string]any
		want bool
	}{
		// string contains
		{"string contains middle", mk("in", "ell", "hello"), true},
		{"string prefix", mk("in", "he", "hello"), true},
		{"string suffix", mk("in", "llo", "hello"), true},
		{"string not contains", mk("in", "xyz", "hello"), false},
		{"string non-str needle", mk("in", 1.0, "hello"), false},
		// array member
		{"array member present num", mk("in", 2.0, []any{1.0, 2.0, 3.0}), true},
		{"array member absent", mk("in", 4.0, []any{1.0, 2.0, 3.0}), false},
		{"array member string", mk("in", "a", []any{"a", "b"}), true},
		{"array empty", mk("in", 1.0, []any{}), false},
		// object key
		{"object key present", mk("in", "name", map[string]any{"name": "x"}), true},
		{"object key absent", mk("in", "foo", map[string]any{"name": "x"}), false},
		{"object non-str needle", mk("in", 1.0, map[string]any{"1": "x"}), false},
		// wrong arg count
		{"in one arg", singleArr("in", "a"), false},
		{"in three args", mk("in", "a", "b", "c"), false},
		// non-collection haystack
		{"in number haystack", mk("in", "a", 1.0), false},
		{"in null haystack", mk("in", "a", nil), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Evaluate(c.expr, nil); got != c.want {
				t.Errorf("Evaluate(%v) = %v; want %v", c.expr, got, c.want)
			}
		})
	}
}

func TestEvaluate_Var(t *testing.T) {
	data := map[string]any{
		"verdict": map[string]any{
			"result":  "pass",
			"score":   0.92,
			"nil_val": nil,
			"evidence": []any{
				map[string]any{"type": "log", "weight": 0.5},
				map[string]any{"type": "metric"},
			},
		},
		"exit_fields": map[string]any{
			"approved": true,
			"tags":     []any{"qa", "ship"},
		},
		"run": map[string]any{
			"context": map[string]any{
				"priority": 5.0,
				"labels":   map[string]any{"env": "prod"},
			},
		},
	}

	cases := []struct {
		name string
		expr map[string]any
		want bool
	}{
		// simple dot path
		{"verdict.result == pass",
			mk("==", map[string]any{"var": "verdict.result"}, "pass"), true},
		{"verdict.result != fail",
			mk("!=", map[string]any{"var": "verdict.result"}, "fail"), true},
		{"verdict.score > 0.8",
			mk(">", map[string]any{"var": "verdict.score"}, 0.8), true},
		{"verdict.score <= 0.95",
			mk("<=", map[string]any{"var": "verdict.score"}, 0.95), true},
		// array index path
		{"verdict.evidence.0.type == log",
			mk("==", map[string]any{"var": "verdict.evidence.0.type"}, "log"), true},
		{"verdict.evidence.1.type == metric",
			mk("==", map[string]any{"var": "verdict.evidence.1.type"}, "metric"), true},
		{"verdict.evidence.0.weight > 0.4",
			mk(">", map[string]any{"var": "verdict.evidence.0.weight"}, 0.4), true},
		// array index out of bounds → nil → not equal
		{"verdict.evidence.9.type OOB",
			mk("==", map[string]any{"var": "verdict.evidence.9.type"}, "log"), false},
		// missing path → nil
		{"missing nested path",
			mk("!=", map[string]any{"var": "verdict.missing.field"}, "pass"), true},
		{"path through scalar → nil",
			mk("==", map[string]any{"var": "verdict.result.foo"}, "x"), false},
		// default value (2-arg array form)
		{"default for missing path",
			mk("==", map[string]any{"var": []any{"verdict.missing", "default"}}, "default"), true},
		{"default for OOB index",
			mk("==", map[string]any{"var": []any{"verdict.evidence.9", "none"}}, "none"), true},
		// 1-arg array form
		{"var 1-arg array form",
			mk("==", map[string]any{"var": []any{"verdict.result"}}, "pass"), true},
		// nested run.context
		{"run.context.priority > 4",
			mk(">", map[string]any{"var": "run.context.priority"}, 4.0), true},
		{"run.context.labels.env == prod",
			mk("==", map[string]any{"var": "run.context.labels.env"}, "prod"), true},
		// exit_fields
		{"exit_fields.approved == true",
			mk("==", map[string]any{"var": "exit_fields.approved"}, true), true},
		// in operator with var-resolved haystack
		{"'qa' in exit_fields.tags (var haystack)",
			mk("in", "qa", map[string]any{"var": "exit_fields.tags"}), true},
		// non-string path
		{"var with non-string path → nil",
			mk("==", map[string]any{"var": 123}, "x"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Evaluate(c.expr, data); got != c.want {
				t.Errorf("Evaluate(%v) = %v; want %v", c.expr, got, c.want)
			}
		})
	}

	// nil data: var resolves to nil unless default provided
	if Evaluate(mk("==", map[string]any{"var": "verdict.result"}, "pass"), nil) {
		t.Error("var on nil data should not equal 'pass'")
	}
	if !Evaluate(mk("==", map[string]any{"var": []any{"verdict.result", "default"}}, "default"), nil) {
		t.Error("var default on nil data should be 'default'")
	}
}

func TestEvaluate_Nested(t *testing.T) {
	data := map[string]any{
		"verdict":     map[string]any{"result": "pass", "score": 0.92},
		"exit_fields": map[string]any{"priority": "high"},
	}

	// Three-level nesting: and { ==, or { >, != } }
	t.Run("three-level and/or positive", func(t *testing.T) {
		expr := map[string]any{"and": []any{
			mk("==", map[string]any{"var": "verdict.result"}, "pass"),
			map[string]any{"or": []any{
				mk(">", map[string]any{"var": "verdict.score"}, 0.95), // false
				mk("!=", map[string]any{"var": "exit_fields.priority"}, "low"), // true
			}},
		}}
		if !Evaluate(expr, data) {
			t.Error("expected true")
		}
	})

	// Negative: and { ==, and { >, > } } where inner > is false
	t.Run("nested with false inner", func(t *testing.T) {
		expr := map[string]any{"and": []any{
			mk("==", map[string]any{"var": "verdict.result"}, "pass"),
			map[string]any{"and": []any{
				mk(">", map[string]any{"var": "verdict.score"}, 0.8), // true
				mk("<", map[string]any{"var": "verdict.score"}, 0.9), // false
			}},
		}}
		if Evaluate(expr, data) {
			t.Error("expected false (inner and fails)")
		}
	})

	// not around nested expression
	t.Run("not around nested expression", func(t *testing.T) {
		expr := singleArr("not", mk("==", map[string]any{"var": "verdict.result"}, "fail"))
		if !Evaluate(expr, data) {
			t.Error("not(verdict==fail) should be true")
		}
	})
}

func TestEvaluate_NoPanic(t *testing.T) {
	// All inputs must return some bool without panicking.
	cases := []struct {
		name string
		expr map[string]any
		data any
	}{
		{"unknown operator", map[string]any{"foo": []any{1, 2}}, nil},
		{"multi-key top", map[string]any{"a": 1, "b": 2}, nil},
		{"empty map", map[string]any{}, nil},
		{"nil-prone: var on int data", mk("==", map[string]any{"var": "a.b"}, "x"), 42},
		{"nil-prone: var on string data", mk("==", map[string]any{"var": "a.b"}, "x"), "hello"},
		{"nil-prone: var on array data", mk("==", map[string]any{"var": "a.b"}, "x"), []any{1.0}},
		{"var with non-string path", mk("==", map[string]any{"var": 123}, "x"), nil},
		{"var path through scalar",
			mk("==", map[string]any{"var": "a.b.c"}, "x"), map[string]any{"a": 1.0}},
		{"var huge index (overflow)",
			mk("==", map[string]any{"var": "a.999999999999999999999999"}, "x"),
			map[string]any{"a": []any{1.0}}},
		{"var non-digit index",
			mk("==", map[string]any{"var": "a.1abc"}, "x"),
			map[string]any{"a": []any{1.0}}},
		{"nested deep type chaos",
			mk("==",
				map[string]any{"var": map[string]any{"nested": "x"}},
				map[string]any{"foo": []any{1, 2}}),
			nil},
		{"literal array as expr element",
			map[string]any{"and": []any{[]any{1.0, 2.0}, "str", 42}}, nil},
		{"object with non-operator key as expr element",
			mk("==", map[string]any{"unknown_op": "x"}, "y"), nil},
		{"non-array args to and",
			map[string]any{"and": "scalar"}, nil},
		{"non-array args to ==",
			map[string]any{"==": 1}, nil},
		{"nested malformed deeply",
			map[string]any{"and": []any{
				map[string]any{"or": []any{
					map[string]any{"not": []any{
						map[string]any{"unknown_op": []any{1, 2}},
					}},
				}},
			}}, nil},
		{"nil data with deep var",
			mk("==", map[string]any{"var": "a.b.c.d.e"}, "x"), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Evaluate panicked: %v\nexpr=%v\ndata=%v", r, c.expr, c.data)
				}
			}()
			_ = Evaluate(c.expr, c.data)
		})
	}
}

func TestEvaluate_TopLevelObjectOnly(t *testing.T) {
	// Evaluate's signature (map[string]any) enforces top-level object at the
	// type level. Here we verify the structural rules the signature can't:
	if Evaluate(map[string]any{}, nil) {
		t.Error("empty top-level map → false")
	}
	if Evaluate(map[string]any{"a": 1, "b": 2}, nil) {
		t.Error("multi-key top-level map → false")
	}
	if Evaluate(map[string]any{"unknown_op": []any{1, 2}}, nil) {
		t.Error("unknown top-level operator → false")
	}
}

func TestValidateSchema_Valid(t *testing.T) {
	cases := map[string]string{
		"==":           `{"==": [{"var": "x"}, 1]}`,
		"!=":           `{"!=": [{"var": "x"}, 1]}`,
		">":            `{">": [1, 2]}`,
		"<":            `{"<": [1, 2]}`,
		">=":           `{">=": [1, 2]}`,
		"<=":           `{"<=": [1, 2]}`,
		"and":          `{"and": [true, false]}`,
		"or":           `{"or": [true, {"var": "x"}]}`,
		"not":          `{"not": [true]}`,
		"in":           `{"in": ["a", ["a", "b"]]}`,
		"var string":   `{"var": "x"}`,
		"var array 1":  `{"var": ["x"]}`,
		"var array 2":  `{"var": ["x", "default"]}`,
		"nested valid": `{"and": [{"==": [{"var": "x"}, 1]}, {"or": [true, false]}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSchema(json.RawMessage(raw)); err != nil {
				t.Errorf("ValidateSchema(%s) unexpected error: %v", raw, err)
			}
		})
	}
}

func TestValidateSchema_Errors(t *testing.T) {
	cases := map[string]string{
		"invalid JSON":        `{not valid`,
		"empty raw":           ``,
		"unknown operator":    `{"foo": [1, 2]}`,
		"wrong arg count ==":  `{"==": [1]}`,
		"wrong arg count >=":  `{">=": [1, 2, 3]}`,
		"wrong arg count not": `{"not": [true, false]}`,
		"wrong arg count var": `{"var": ["a", "b", "c"]}`,
		"not object array":    `[1, 2, 3]`,
		"not object string":   `"hello"`,
		"not object number":   `42`,
		"not object null":     `null`,
		"not object bool":     `true`,
		"empty object":        `{}`,
		"multi-key object":    `{"==": [1, 1], "!=": [1, 2]}`,
		"var non-string path": `{"var": [123]}`,
		"non-array args ==":   `{"==": 1}`,
		"non-array args and":  `{"and": true}`,
		"nested invalid":      `{"and": [{"==": [1]}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateSchema(json.RawMessage(raw))
			if err == nil {
				t.Errorf("ValidateSchema(%s) expected error, got nil", raw)
			}
		})
	}
}

func TestSupportedOperators(t *testing.T) {
	ops := SupportedOperators()
	// PRD R1 miscounts as "10"; the enumerated set has 11.
	if len(ops) != 11 {
		t.Errorf("expected 11 operators (PRD miscount), got %d: %v", len(ops), ops)
	}
	seen := map[string]bool{}
	for _, op := range ops {
		if seen[op] {
			t.Errorf("duplicate operator in SupportedOperators(): %s", op)
		}
		seen[op] = true
	}
	expected := []string{"==", "!=", ">", "<", ">=", "<=", "and", "or", "not", "in", "var"}
	for _, op := range expected {
		if !seen[op] {
			t.Errorf("missing operator from SupportedOperators(): %s", op)
		}
	}
}
