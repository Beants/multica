package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// SetStatus transitions an issue to a new status outside the HTTP update
// path while carrying the side effects the board and timeline rely on:
//
//  1. a workspace-guarded UPDATE,
//  2. an issue:updated broadcast (map payload, same shape as
//     TaskService.broadcastIssueUpdated — the WS fanout marshals it as-is,
//     which is what drives the realtime column reconcile),
//  3. a status_changed activity row written directly plus its
//     activity:created broadcast. The activity listener only type-asserts
//     handler.IssueResponse payloads, which this layer cannot produce
//     without an import cycle (handler imports service), so the row is
//     written here in the listener's shape instead.
//
// It is deliberately narrower than handler.UpdateIssue: no WillEnqueueRun
// dispatch (the workflow engine manages its own task lifecycle and already
// enqueued explicitly) and no child-done parent notification (the engine
// owns its intake-parent semantics). Callers are non-HTTP services — today
// only the workflow engine (design.md §4.1: the backlog → todo flip after a
// node child issue is handoff-enqueued, and the terminal done/cancelled
// flips when a step finishes).
func (s *IssueService) SetStatus(ctx context.Context, issue db.Issue, newStatus string) (db.Issue, error) {
	if issue.Status == newStatus {
		return issue, nil
	}
	updated, err := s.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID:          issue.ID,
		Status:      newStatus,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return db.Issue{}, fmt.Errorf("update issue status: %w", err)
	}
	prevStatus := issue.Status

	details, _ := json.Marshal(map[string]string{"from": prevStatus, "to": newStatus})
	activity, actErr := s.Queries.CreateActivity(ctx, db.CreateActivityParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
		ActorType:   util.StrToText("system"),
		Action:      "status_changed",
		Details:     details,
	})
	if actErr != nil {
		slog.Warn("issue SetStatus: activity write failed",
			"issue_id", util.UUIDToString(issue.ID), "error", actErr)
	} else {
		s.publishActivityCreated(issue, activity)
	}

	s.publishIssueUpdated(ctx, updated, map[string]any{
		"status_changed": true,
		"prev_status":    prevStatus,
	})
	return updated, nil
}

// ReassignToMember hands an issue to a workspace member (human take-over)
// and broadcasts issue:updated with assignee_changed so the board and the
// new assignee's views reconcile. The workflow engine's circuit-breaker and
// escalation paths use it to park the intake parent with a human
// (design.md §4.4). All other fields are preserved verbatim from the
// caller's read of the row.
func (s *IssueService) ReassignToMember(ctx context.Context, issue db.Issue, userID pgtype.UUID) (db.Issue, error) {
	updated, err := s.Queries.UpdateIssue(ctx, db.UpdateIssueParams{
		ID:            issue.ID,
		AssigneeType:  util.StrToText("member"),
		AssigneeID:    userID,
		StartDate:     issue.StartDate,
		DueDate:       issue.DueDate,
		ParentIssueID: issue.ParentIssueID,
		ProjectID:     issue.ProjectID,
		Stage:         issue.Stage,
	})
	if err != nil {
		return db.Issue{}, fmt.Errorf("reassign issue: %w", err)
	}
	s.publishIssueUpdated(ctx, updated, map[string]any{
		"assignee_changed":   true,
		"prev_assignee_type": util.TextToPtr(issue.AssigneeType),
		"prev_assignee_id":   util.UUIDToPtr(issue.AssigneeID),
	})
	return updated, nil
}

// publishIssueUpdated emits the issue:updated event with the issue payload
// merged in. Payload is map-shaped (issueToMap) for the same reason as
// TaskService.broadcastIssueUpdated: the WS fanout needs it for client
// cache reconciliation; listener-side activity/inbox paths intentionally do
// not fire on map payloads.
func (s *IssueService) publishIssueUpdated(ctx context.Context, issue db.Issue, payload map[string]any) {
	if s.Bus == nil {
		return
	}
	payload["issue"] = issueToMap(issue, s.issuePrefix(ctx, issue.WorkspaceID))
	s.Bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		Payload:     payload,
	})
}

// publishActivityCreated mirrors cmd/server's publishActivityEvent payload
// ({ issue_id, entry: TimelineEntry }) so timeline subscribers render a
// service-layer status change exactly like a listener-recorded one.
func (s *IssueService) publishActivityCreated(issue db.Issue, activity db.ActivityLog) {
	if s.Bus == nil {
		return
	}
	actorType := ""
	if activity.ActorType.Valid {
		actorType = activity.ActorType.String
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventActivityCreated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		Payload: map[string]any{
			"issue_id": util.UUIDToString(issue.ID),
			"entry": map[string]any{
				"type":       "activity",
				"id":         util.UUIDToString(activity.ID),
				"actor_type": actorType,
				"actor_id":   util.UUIDToString(activity.ActorID),
				"action":     activity.Action,
				"details":    json.RawMessage(activity.Details),
				"created_at": util.TimestampToString(activity.CreatedAt),
			},
		},
	})
}

func (s *IssueService) issuePrefix(ctx context.Context, workspaceID pgtype.UUID) string {
	ws, err := s.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return ""
	}
	return ws.IssuePrefix
}
