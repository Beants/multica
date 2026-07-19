package workflow

// template_fanout.go — P1-1 Wave 1 fan_out publish-time validation +
// subtask list schema. Wave 2 adds the activate-time fan_out engine
// (see fanout.go, planned); this file owns the data shapes the engine
// will consume: SubtaskItem, SubtaskFieldError, parseSubtasks, and the
// two publish-time topology validators invoked by validateTemplateGraph
// (ValidateFanOutConfig, ValidateConvergePairing).

import (
	"encoding/json"
	"fmt"
)

// SubtaskItem is one entry in the upstream agent's exit_fields[items_field]
// array. The Wave 2 fan_out engine expands N of these into N child
// StepInstances (each gets its own child issue + agent task dispatch).
//
// Field mapping (PRD R2):
//   - Title        → child issue.title (required)
//   - Instructions → EnqueueTaskForIssueWithHandoff.handoffNote (required)
//   - AgentSelector → resolved to AgentID at fan_out trigger time →
//     child issue.assignee_id (required; not resolved at publish because
//     the list items do not exist until the upstream agent submits)
//   - Priority → issue.priority (optional, defaults to "none")
//   - DueDate  → issue.due_date (optional, ISO 8601 string)
//   - Labels   → issue_to_label associations by name (optional; workspace
//     label existence is validated at fan_out trigger time, not here)
type SubtaskItem struct {
	Title         string   `json:"title"`
	Instructions  string   `json:"instructions"`
	AgentSelector string   `json:"agent_selector"`
	Priority      string   `json:"priority,omitempty"`
	DueDate       string   `json:"due_date,omitempty"`
	Labels        []string `json:"labels,omitempty"`
}

// SubtaskFieldError is one structured validation failure for a subtask
// item. It follows P0 FieldError's shape with the addition of ItemIndex
// (the position of the offending entry inside the items array) so callers
// can pinpoint which item failed in a 422 response.
//
// Code is "missing" for an absent required field, "invalid" for a present
// field with an unacceptable value (e.g. priority outside the enum).
type SubtaskFieldError struct {
	ItemIndex int    `json:"item_index"`
	Name      string `json:"name"`
	Code      string `json:"code"`
	Expected  string `json:"expected,omitempty"`
	Message   string `json:"message"`
}

// Allowed subtask Priority values (mirrors multica issue.priority enum).
const (
	SubtaskPriorityUrgent = "urgent"
	SubtaskPriorityHigh   = "high"
	SubtaskPriorityMedium = "medium"
	SubtaskPriorityLow    = "low"
	SubtaskPriorityNone   = "none"
)

// parseSubtasks validates upstream exit_fields[items_field] array entries
// and returns the decoded items alongside any field-level errors. The
// caller (Wave 2 fan_out engine, at trigger time — NOT publish time,
// because the list items do not exist when the template is published)
// decides whether non-empty errs abort the whole fan_out expansion.
//
// Semantics:
//   - Each item must have non-empty title, instructions, agent_selector.
//   - priority, if non-empty, must be one of urgent|high|medium|low|none.
//   - agent_selector resolution and label existence are NOT checked here;
//     those run at fan_out trigger time after parseSubtasks succeeds.
//
// Items with structural errors (marshal/unmarshal failure) are skipped;
// items with field-level errors are still appended to the returned slice
// so callers can opt to surface partial parses if desired.
func parseSubtasks(raw []any) ([]SubtaskItem, []SubtaskFieldError) {
	items := make([]SubtaskItem, 0, len(raw))
	var errs []SubtaskFieldError
	for i, r := range raw {
		itemBytes, err := json.Marshal(r)
		if err != nil {
			errs = append(errs, SubtaskFieldError{
				ItemIndex: i,
				Name:      "$",
				Code:      "invalid",
				Message:   fmt.Sprintf("item %d: marshal failed: %v", i, err),
			})
			continue
		}
		var item SubtaskItem
		if err := json.Unmarshal(itemBytes, &item); err != nil {
			errs = append(errs, SubtaskFieldError{
				ItemIndex: i,
				Name:      "$",
				Code:      "invalid",
				Message:   fmt.Sprintf("item %d: %v", i, err),
			})
			continue
		}
		if item.Title == "" {
			errs = append(errs, SubtaskFieldError{
				ItemIndex: i, Name: "title", Code: "missing",
				Message: "title is required",
			})
		}
		if item.Instructions == "" {
			errs = append(errs, SubtaskFieldError{
				ItemIndex: i, Name: "instructions", Code: "missing",
				Message: "instructions is required",
			})
		}
		if item.AgentSelector == "" {
			errs = append(errs, SubtaskFieldError{
				ItemIndex: i, Name: "agent_selector", Code: "missing",
				Message: "agent_selector is required",
			})
		}
		if item.Priority != "" {
			switch item.Priority {
			case SubtaskPriorityUrgent, SubtaskPriorityHigh, SubtaskPriorityMedium,
				SubtaskPriorityLow, SubtaskPriorityNone:
			default:
				errs = append(errs, SubtaskFieldError{
					ItemIndex: i, Name: "priority", Code: "invalid",
					Expected: "urgent|high|medium|low|none",
					Message:  fmt.Sprintf("priority %q not in allowed enum", item.Priority),
				})
			}
		}
		items = append(items, item)
	}
	return items, errs
}

// ValidateFanOutConfig enforces fan_out publish-time config invariants
// (PRD R6 / AC1):
//   - cfg.ItemsField must be non-empty (the array exit_field the fan_out
//     consumes);
//   - the fan_out's upstream node(s) — discovered via inbound edges — must
//     declare a matching ExitFieldsSchema field with type=array or type=any
//     (any is permitted for forward-compat with loosely-typed schemas).
//
// agent_selector resolution, label existence, and per-item required fields
// are runtime checks (PRD R3, design §5); publish only validates the
// schema shape because the list items themselves do not exist yet.
//
// allNodes / allEdges are the full template inputs (same slice content
// validateTemplateGraph already validated).
func ValidateFanOutConfig(fanOutNode NodeInput, cfg NodeConfig, allNodes []NodeInput, allEdges []EdgeInput) error {
	if cfg.ItemsField == "" {
		return fmt.Errorf("workflow: fan_out node %q requires config.items_field", fanOutNode.NodeKey)
	}
	// Locate the fan_out's upstream node(s) via inbound edges. fan_out can
	// have multiple inbound edges in arbitrary DAGs, but at least one of
	// them must declare the items_field array.
	upstreamKeys := map[string]bool{}
	for _, e := range allEdges {
		if e.ToNodeKey == fanOutNode.NodeKey {
			upstreamKeys[e.FromNodeKey] = true
		}
	}
	if len(upstreamKeys) == 0 {
		return fmt.Errorf("workflow: fan_out node %q has no upstream node (items_field %q cannot be resolved)",
			fanOutNode.NodeKey, cfg.ItemsField)
	}
	for _, up := range allNodes {
		if !upstreamKeys[up.NodeKey] {
			continue
		}
		upCfg, err := ParseNodeConfig(up.Config)
		if err != nil {
			continue // surfaced by the main validator; skip here
		}
		if upCfg.ExitFields == nil {
			continue
		}
		for _, f := range upCfg.ExitFields.Fields {
			if f.Name == cfg.ItemsField && (f.Type == "array" || f.Type == "any") {
				return nil
			}
		}
	}
	return fmt.Errorf("workflow: fan_out node %q items_field %q not declared as array on any upstream node",
		fanOutNode.NodeKey, cfg.ItemsField)
}

// ValidateConvergePairing enforces the fan_out↔converge topology invariant
// (PRD / AC2): every fan_out node must reach at least one converge node via
// downstream edges, and every converge node must be reachable from at least
// one fan_out node via upstream edges. 1:1 pairing is NOT required (one
// fan_out may feed a single converge; one converge may receive from multiple
// fan_outs).
//
// Reachability is transitive (BFS), not direct-edge: the canonical P1 shape
// is `fan_out → branchA/B → converge`, where fan_out has no direct edge to
// converge — only via its branch children. A direct-edge check would
// spuriously reject that shape.
//
// P0 templates have neither fan_out nor converge, so this is a no-op for
// them — preserving P0 backward compatibility.
func ValidateConvergePairing(allNodes []NodeInput, allEdges []EdgeInput) error {
	fanOutKeys := map[string]bool{}
	convergeKeys := map[string]bool{}
	for _, n := range allNodes {
		switch n.Type {
		case NodeTypeFanOut:
			fanOutKeys[n.NodeKey] = true
		case NodeTypeConverge:
			convergeKeys[n.NodeKey] = true
		}
	}
	if len(fanOutKeys) == 0 && len(convergeKeys) == 0 {
		return nil // P0 template; nothing to check
	}
	downstream := map[string][]string{}
	upstream := map[string][]string{}
	for _, e := range allEdges {
		downstream[e.FromNodeKey] = append(downstream[e.FromNodeKey], e.ToNodeKey)
		upstream[e.ToNodeKey] = append(upstream[e.ToNodeKey], e.FromNodeKey)
	}
	for fo := range fanOutKeys {
		if !bfsReachAnyTarget(fo, downstream, convergeKeys) {
			return fmt.Errorf("workflow: fan_out node %q has no downstream path to a converge node", fo)
		}
	}
	for c := range convergeKeys {
		if !bfsReachAnyTarget(c, upstream, fanOutKeys) {
			return fmt.Errorf("workflow: converge node %q has no upstream path from a fan_out node", c)
		}
	}
	return nil
}

// bfsReachAnyTarget walks adj from start (exclusive) and reports whether any
// reachable node is in targets. Cycle-safe via the visited map.
func bfsReachAnyTarget(start string, adj map[string][]string, targets map[string]bool) bool {
	visited := map[string]bool{start: true}
	queue := append([]string(nil), adj[start]...)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur] {
			continue
		}
		visited[cur] = true
		if targets[cur] {
			return true
		}
		queue = append(queue, adj[cur]...)
	}
	return false
}
