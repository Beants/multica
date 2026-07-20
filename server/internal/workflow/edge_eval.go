// Package workflow — edge_eval.go (P1-2 Wave 2)
//
// buildEvalCtx assembles the JSONLogic evaluation context (blueprint TS-8
// three-namespace freeze) from the runtime state surrounding a verdict/
// acceptance transition. The shape is fixed:
//
//	{
//	  verdict:    {result, root_cause, verdict_by, evidence},
//	  exit_fields:{... submission.ExitFields decoded ...},
//	  run:        {context: {initiator_id, reviewer_id}}
//	}
//
// verdict / submission may be nil:
//   - StartRun: no verdict yet (startNode edge conditions should be nil, but
//     an empty context makes any var reference resolve to nil → conditions
//     fall through to catch-all).
//   - decideAcceptanceTx Approve: acceptance edges must be catch-all (R7),
//     but we still pass an empty verdict map for symmetry.
//
// Any decode failure degrades to an empty namespace (PRD R5: var references
// against missing data return nil → conditions don't match → catch-all path).
package workflow

import (
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// buildEvalCtx assembles the JSONLogic evaluation context from the runtime
// state. verdict / submission may be nil; runCtx is always carried (zero
// RunContext is the system-triggered case).
func buildEvalCtx(verdict *db.Verdict, submission *db.Submission, runCtx RunContext) map[string]any {
	ctx := map[string]any{}
	if verdict != nil {
		var evidence map[string]any
		if len(verdict.Evidence) > 0 {
			_ = json.Unmarshal(verdict.Evidence, &evidence)
		}
		ctx["verdict"] = map[string]any{
			"result":     verdict.Result,
			"root_cause": textOrEmpty(verdict.RootCause),
			"verdict_by": verdict.VerdictBy,
			"evidence":   evidence,
		}
	}
	if submission != nil && len(submission.ExitFields) > 0 {
		var fields map[string]any
		if err := json.Unmarshal(submission.ExitFields, &fields); err == nil {
			ctx["exit_fields"] = fields
		}
	}
	ctx["run"] = map[string]any{
		"context": map[string]any{
			"initiator_id": runCtx.InitiatorID,
			"reviewer_id":  runCtx.ReviewerID,
		},
	}
	return ctx
}

// textOrEmpty unwraps a nullable pgtype.Text; invalid → "" (matches the
// JSONLogic truthiness rule for empty strings).
func textOrEmpty(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}
