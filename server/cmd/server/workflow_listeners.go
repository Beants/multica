package main

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// workflow_listeners.go — task-event → workflow-engine mapping (P0 fork
// file; design.md §4.4). Mirrors autopilot_listeners.go: thin subscriptions
// that translate daemon task lifecycle events into engine entry points,
// keyed by step_instance.agent_task_id. Two guards keep the blast radius at
// zero for non-workflow traffic:
//   - the workflow_engine flag is evaluated per event with the event's
//     workspace (AC6: flag off == never subscribed), and
//   - engine entry points no-op on any task not bound to a step (one
//     indexed lookup — the common case by far).

// newWorkflowEngineForListeners builds the engine for main.go's listener
// registration. Kept here so main.go needs no workflow import and stays
// inside the fork's +2-line touch budget (design.md §2).
func newWorkflowEngineForListeners(h *handler.Handler) *workflow.Engine {
	return workflow.NewEngine(h.Queries, h.TxStarter, h.IssueService, h.TaskService, h.Bus)
}

// registerWorkflowListeners subscribes the engine to the daemon task
// lifecycle (design.md §4.4):
//   - task:dispatch  → step active → dispatched (guarded)
//   - task:failed    → step failed; retry while attempt < max_attempts, else
//     run paused + human handoff
//   - task:completed → without a submission: step blocked + run paused +
//     inbox notification (P0 minimal: checked at completion time, no timers)
func registerWorkflowListeners(bus *events.Bus, eng *workflow.Engine, flags *featureflag.Service) {
	ctx := context.Background()

	enabled := func(workspaceID string) bool {
		evalCtx := featureflag.WithEvalContext(ctx, featureflag.EvalContext{WorkspaceID: workspaceID})
		return flags.IsEnabled(evalCtx, workflow.FlagEngine, false)
	}
	taskIDOf := func(e events.Event) (pgtype.UUID, bool) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return pgtype.UUID{}, false
		}
		raw, _ := payload["task_id"].(string)
		id, err := util.ParseUUID(raw)
		if err != nil {
			return pgtype.UUID{}, false
		}
		return id, true
	}
	handle := func(e events.Event, fn func(context.Context, pgtype.UUID) error) {
		if !enabled(e.WorkspaceID) {
			return
		}
		taskID, ok := taskIDOf(e)
		if !ok {
			return
		}
		if err := fn(ctx, taskID); err != nil {
			slog.Warn("workflow listener: event mapping failed",
				"event", e.Type, "task_id", util.UUIDToString(taskID), "error", err)
		}
	}

	bus.Subscribe(protocol.EventTaskDispatch, func(e events.Event) {
		handle(e, eng.HandleTaskDispatch)
	})
	bus.Subscribe(protocol.EventTaskFailed, func(e events.Event) {
		handle(e, eng.HandleTaskFailed)
	})
	bus.Subscribe(protocol.EventTaskCompleted, func(e events.Event) {
		handle(e, eng.HandleTaskCompleted)
	})
}
