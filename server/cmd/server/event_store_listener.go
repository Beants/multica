package main

// event_store_listener.go — P2-1: subscribes to every published event and
// appends it to event_store. P2-3 metrics / P2-2 webhooks / P2-4 dashboard
// all read from this store instead of pulling business tables.
//
// Best-effort: insert failures are logged, never propagated. The store must
// not stall the in-process event bus (Publish is synchronous; a panicking
// listener would break every publisher).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// registerEventStoreListener appends a SubscribeAll handler that persists
// every event. Idempotent via the dedup_key unique index (938) + ON CONFLICT.
func registerEventStoreListener(bus *events.Bus, queries *db.Queries) {
	bus.SubscribeAll(func(e events.Event) {
		payload, err := json.Marshal(e.Payload)
		if err != nil {
			payload = []byte("{}")
		}
		wsID, _ := util.ParseUUID(e.WorkspaceID)
		actorID, _ := util.ParseUUID(e.ActorID)
		ev, err := queries.InsertEvent(context.Background(), db.InsertEventParams{
			WorkspaceID: wsID,
			EventType:   e.Type,
			ActorType:   pgtype.Text{String: e.ActorType, Valid: e.ActorType != ""},
			ActorID:     actorID,
			Payload:     payload,
			DedupKey:    eventDedupKey(e.Type, e.WorkspaceID, e.ActorType, e.ActorID, payload),
		})
		if err != nil {
			slog.Warn("event_store: insert failed", "type", e.Type, "error", err)
		} else {
			// P2-2: fan out to outbound webhooks (goroutine-ized internally —
			// never blocks the bus on a slow external endpoint).
			go deliverEventToWebhooks(queries, ev)
		}
	})
}

// eventDedupKey hashes the event identity so a genuine retry (same event
// re-published) collapses via ON CONFLICT, while two distinct events of the
// same type still both land. Truncated to 16 hex chars (64 bits) — enough
// collision resistance for an append-only event log.
func eventDedupKey(typ, wsID, actorType, actorID string, payload []byte) string {
	h := sha256.Sum256([]byte(typ + "|" + wsID + "|" + actorType + "|" + actorID + "|" + string(payload)))
	return hex.EncodeToString(h[:])[:16]
}
