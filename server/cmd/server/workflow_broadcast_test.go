package main

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/workflow"
)

// workflow_broadcast_test.go — 3.5a wiring proof: the fork-declared workflow
// event types (internal/workflow/events.go — deliberately NOT registered in
// the upstream pkg/protocol/events.go) flow through the unchanged
// registerListeners SubscribeAll path to the workspace room. The broadcast
// path is type-agnostic; this test pins that contract so an upstream change
// to a type-registry model would fail loudly here instead of silently
// dropping workflow events.

func TestWorkflowEventsBroadcastToWorkspace(t *testing.T) {
	cases := []struct {
		name    string
		event   events.Event
		wantTyp string
	}{
		{
			name: "run-updated",
			event: events.Event{
				Type:        workflow.EventRunUpdated,
				WorkspaceID: "ws-1",
				ActorType:   "system",
				Payload:     map[string]any{"run_id": "run-1", "status": "paused"},
			},
			wantTyp: "workflow:run-updated",
		},
		{
			name: "step-updated",
			event: events.Event{
				Type:        workflow.EventStepUpdated,
				WorkspaceID: "ws-1",
				ActorType:   "system",
				Payload:     map[string]any{"run_id": "run-1", "step_instance_id": "step-1", "status": "passed"},
			},
			wantTyp: "workflow:step-updated",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus := events.New()
			fb := &fakeBroadcaster{}
			registerListeners(bus, fb)

			bus.Publish(tc.event)

			if len(fb.workspaceCalls) != 1 {
				t.Fatalf("BroadcastToWorkspace calls = %d, want 1 (fork event types must reach the workspace room)", len(fb.workspaceCalls))
			}
			if fb.workspaceCalls[0].workspaceID != "ws-1" {
				t.Fatalf("broadcast workspace = %q, want ws-1", fb.workspaceCalls[0].workspaceID)
			}
			var frame map[string]any
			if err := json.Unmarshal(fb.workspaceCalls[0].msg, &frame); err != nil {
				t.Fatalf("unmarshal frame: %v", err)
			}
			if frame["type"] != tc.wantTyp {
				t.Fatalf("frame type = %v, want %q", frame["type"], tc.wantTyp)
			}
			payload, ok := frame["payload"].(map[string]any)
			if !ok {
				t.Fatalf("frame payload missing or not an object: %v", frame["payload"])
			}
			if payload["run_id"] != "run-1" {
				t.Fatalf("payload run_id = %v, want run-1", payload["run_id"])
			}
		})
	}
}
