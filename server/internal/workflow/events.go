package workflow

import (
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// events.go — WS event emission for the workflow engine (P0 fork file).
//
// The event TYPE constants live here and not in pkg/protocol/events.go (an
// upstream file) because the bus (internal/events) and the SubscribeAll
// broadcast path (cmd/server/listeners.go) are type-agnostic: any event with
// a WorkspaceID is marshalled verbatim and fanned out to the workspace room.
// Frontend consumers match on these exact strings
// (packages/core/types/events.ts; payload shapes in
// packages/core/types/workflow-events.ts).
//
// Payloads are id-carriers: the frontend invalidates the run's React Query
// keys and refetches — `status` is a hint, never authoritative state.
//
// Emission rule (3.5a): every COMMITTED run/step status change publishes one
// event. Publishes happen strictly after tx.Commit — an event must never
// announce a transition that could still roll back (a pre-commit publish
// would send clients refetching stale state with no follow-up event).
// Statuses covered: run created(running)/paused/waiting_acceptance/
// completed (failed/cancelled are declared but never set in P0); step
// active/dispatched/passed/failed/blocked/rework/skipped (pending
// pre-creations are skipped deliberately: every pending row commits alongside
// a sibling active/run event that triggers the same invalidation).
const (
	EventRunUpdated  = "workflow:run-updated"
	EventStepUpdated = "workflow:step-updated"
)

// publishRunUpdated emits the workspace-scoped run event. Nil-bus safe (the
// engine is always constructed with a bus in production, but tests build
// engines without one — same guard shape as publishIssueCreated).
func (e *Engine) publishRunUpdated(run db.WorkflowRun, status string) {
	if e.Bus == nil {
		return
	}
	e.Bus.Publish(events.Event{
		Type:        EventRunUpdated,
		WorkspaceID: util.UUIDToString(run.WorkspaceID),
		ActorType:   "system",
		Payload: map[string]any{
			"run_id": util.UUIDToString(run.ID),
			"status": status,
		},
	})
}

// publishStepUpdated emits the workspace-scoped step event. runID is carried
// in the payload so the detail page can ignore events for other runs without
// a lookup.
func (e *Engine) publishStepUpdated(run db.WorkflowRun, stepID pgtype.UUID, status string) {
	if e.Bus == nil {
		return
	}
	e.Bus.Publish(events.Event{
		Type:        EventStepUpdated,
		WorkspaceID: util.UUIDToString(run.WorkspaceID),
		ActorType:   "system",
		Payload: map[string]any{
			"run_id":           util.UUIDToString(run.ID),
			"step_instance_id": util.UUIDToString(stepID),
			"status":           status,
		},
	})
}
