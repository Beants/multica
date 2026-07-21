package main

// outbound_delivery.go — P2-2 delivery path. After an event lands in
// event_store, this finds the workspace's active outbound webhooks whose
// event_types cover it and POSTs the payload with an HMAC-SHA256 signature,
// recording each delivery's outcome (status/response_code) in
// outbound_delivery.
//
// MVP: one synchronous POST per (webhook, event), no automatic backoff
// retry (a failed delivery records status=failed + attempts=1; a dedicated
// retry worker is a follow-up). Delivery runs in its own goroutine so the
// event_store listener's insert is never blocked by a slow external endpoint.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// deliverEventToWebhooks fans out one event_store row to every matching
// webhook. Spawned by the event_store listener after a successful InsertEvent.
func deliverEventToWebhooks(queries *db.Queries, event db.EventStore) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !event.WorkspaceID.Valid {
		return
	}
	whs, err := queries.ListWebhooksForEvent(ctx, db.ListWebhooksForEventParams{
		WorkspaceID: event.WorkspaceID,
		Column2:     event.EventType,
	})
	if err != nil {
		slog.Warn("outbound: list webhooks failed", "type", event.EventType, "error", err)
		return
	}
	for _, wh := range whs {
		go deliverOne(queries, wh, event)
	}
}

func deliverOne(queries *db.Queries, wh db.OutboundWebhook, event db.EventStore) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	body := event.Payload
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.Url, bytes.NewReader(body))
	if err != nil {
		recordDelivery(queries, wh.ID, event.ID, "failed", 0, false)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Multica-Signature", hmacHex(wh.Secret, body))
	req.Header.Set("X-Multica-Event-Type", event.EventType)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("outbound: deliver failed", "webhook", wh.ID, "error", err)
		recordDelivery(queries, wh.ID, event.ID, "failed", 0, false)
		return
	}
	defer resp.Body.Close()
	code := int32(resp.StatusCode)
	status := "delivered"
	if code < 200 || code >= 300 {
		status = "failed"
	}
	recordDelivery(queries, wh.ID, event.ID, status, code, true)
}

func recordDelivery(queries *db.Queries, webhookID, eventID pgtype.UUID, status string, code int32, validCode bool) {
	if _, err := queries.InsertDelivery(context.Background(), db.InsertDeliveryParams{
		WebhookID: webhookID,
		EventID:   eventID,
		Status:    status,
		Attempts:  1,
		ResponseCode: pgtype.Int4{Int32: code, Valid: validCode},
		DeliveredAt:  pgtype.Timestamptz{Time: time.Now(), Valid: status == "delivered"},
	}); err != nil {
		slog.Warn("outbound: insert delivery failed", "error", err)
	}
}

func hmacHex(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
