package workflow

// capability.go — P1-7 capability routing. resolveAgentForNode picks the
// agent for an agent-node at activation time by the node's DispatchStrategy:
//
//   - specified/fallback: node.Config.AgentID (publish-resolved default)
//   - capability: MatchAgentByCapability(required_capabilities) → highest-
//     total-proficiency workspace agent declaring ALL required keys; on no
//     match (or query error) it falls back to node.Config.AgentID so a
//     missing capability declaration never dead-ends dispatch.
//
// Capability is the only strategy that resolves at runtime (proficiency is
// dynamic) — the documented D-7 carve-out. The upstream strategy (assignee
// carried in the prior step's exit_fields) needs the exit_fields data-flow
// plumbing and is out of P1-7 MVP scope.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// resolveAgentForNode returns the agent UUID to dispatch a step to, by the
// node's DispatchStrategy. Errors only when no strategy resolves an agent
// AND no fallback AgentID is frozen on the node.
func (e *Engine) resolveAgentForNode(ctx context.Context, run db.WorkflowRun, node *SnapshotNode) (pgtype.UUID, error) {
	fallbackID, fbErr := util.ParseUUID(node.Config.AgentID)

	if node.Config.DispatchStrategy == DispatchStrategyCapability && len(node.Config.RequiredCapabilities) > 0 {
		matched, err := e.Queries.MatchAgentByCapability(ctx, db.MatchAgentByCapabilityParams{
			WorkspaceID: run.WorkspaceID,
			Column2:     node.Config.RequiredCapabilities,
		})
		switch {
		case err == nil:
			return matched, nil
		case errors.Is(err, pgx.ErrNoRows):
			slog.Info("workflow: capability match found no qualifying agent; falling back",
				"node_key", node.NodeKey, "required", node.Config.RequiredCapabilities)
		default:
			// Transient DB error: do not fail the whole activation on a
			// capability-lookup hiccup — fall back to the frozen agent so the
			// run keeps moving. The miss is logged for follow-up.
			slog.Warn("workflow: capability match query failed; falling back",
				"node_key", node.NodeKey, "error", err)
		}
	}

	if fbErr != nil {
		return pgtype.UUID{}, fmt.Errorf("node %q has no agent: dispatch_strategy=%q resolved no agent and no fallback agent_id is frozen (republish the template or set required_capabilities)",
			node.NodeKey, node.Config.DispatchStrategy)
	}
	return fallbackID, nil
}
