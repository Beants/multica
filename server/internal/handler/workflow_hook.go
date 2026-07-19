package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/workflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// workflow_hook.go — inbound workflow hooks (R6) plus their management API.
//
// Ingress: POST /api/hooks/workflow/{token} is a PUBLIC route — the bearer
// token in the URL path IS the credential, mirroring the autopilot webhook
// (handler/autopilot_webhook.go:305-342). The token is stored as a SHA-256
// hash (never cleartext) and lookup is hash equality against workflow_hook.
// Idempotency: the engine's UNIQUE(workspace, source_type, source_id,
// template_id) makes a repeated push return the existing run (200) instead
// of creating a duplicate (blueprint §8.3). Delivery audit is the P0
// downgrade (design.md §1): last_used_at + a structured log line, no
// delivery table.
//
// Management: POST/GET /api/workflow-hooks + POST /api/workflow-hooks/{id}/disable
// are workspace-scoped human APIs (session/PAT via RequireWorkspaceMember).
// The cleartext token is returned exactly once, at creation.

// hookTokenPrefix makes a leaked workflow-hook token recognisable in logs
// without revealing the entropy bytes (same rationale as awt_).
const hookTokenPrefix = "wfh_"

// WorkflowHookHandler serves both the public ingress and the management
// API. Kept separate from Handler so the fork adds zero fields to the
// upstream struct; router_workflow.go wires it.
type WorkflowHookHandler struct {
	Queries      *db.Queries
	Engine       *workflow.Engine
	FeatureFlags *featureflag.Service
	IPLimiter    WebhookRateLimiter
	TokenLimiter WebhookRateLimiter
	ClientIP     func(*http.Request) string
}

func NewWorkflowHookHandler(
	q *db.Queries,
	eng *workflow.Engine,
	flags *featureflag.Service,
	ipLimiter WebhookRateLimiter,
	tokenLimiter WebhookRateLimiter,
	clientIP func(*http.Request) string,
) *WorkflowHookHandler {
	return &WorkflowHookHandler{
		Queries:      q,
		Engine:       eng,
		FeatureFlags: flags,
		IPLimiter:    ipLimiter,
		TokenLimiter: tokenLimiter,
		ClientIP:     clientIP,
	}
}

// ClientIPForRateLimit exposes the autopilot webhook's trusted-proxy-aware
// client-IP extractor so router_workflow.go (package main) can wire it into
// the hook handler — the unexported method cannot cross the package boundary.
func (h *Handler) ClientIPForRateLimit(r *http.Request) string {
	return h.clientIPForRateLimit(r)
}

// hashHookToken SHA-256 hashes the cleartext hook token for the DB lookup.
// The cleartext is never stored (design.md §1, deviation #1).
func hashHookToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// generateHookToken returns a cryptographically random bearer token used as
// the public hook URL secret ("wfh_" + URL-safe base64(32 bytes)).
func generateHookToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hookTokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// ---------------------------------------------------------------------------
// POST /api/hooks/workflow/{token} — public ingress
// ---------------------------------------------------------------------------

// workflowHookPayload is the inbound hook body (design.md §3). SourceID is
// the external work identifier and the idempotency key — required. Reviewer
// is optional and resolves to a workspace member (member id or email).
// TemplateKey overrides the hook's bound template when present.
type workflowHookPayload struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	SourceID    string `json:"source_id"`
	Reviewer    string `json:"reviewer"`
	SourceURL   string `json:"source_url"`
	TemplateKey string `json:"template_key"`
}

type workflowHookResponse struct {
	RunID         string `json:"run_id"`
	IntakeIssueID string `json:"intake_issue_id"`
	IssueNumber   int32  `json:"issue_number"`
	Created       bool   `json:"created"`
}

// HandleInboundHook receives one external work item and starts (or replays)
// its workflow run. Flow mirrors HandleAutopilotWebhook: per-IP rate limit →
// token lookup → per-token rate limit → payload → idempotent dispatch.
// The workflow_engine flag is evaluated with the hook's workspace as soon as
// the token resolves it — while off, every outcome is a 404 (AC6).
func (h *WorkflowHookHandler) HandleInboundHook(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		writeError(w, http.StatusNotFound, "hook not found")
		return
	}

	// Per-IP gate before any DB I/O (bounds token-spraying probes).
	if h.IPLimiter != nil && h.ClientIP != nil {
		if ip := h.ClientIP(r); ip != "" {
			if !h.IPLimiter.Allow(r.Context(), ip) {
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
		}
	}

	hook, err := h.Queries.GetWorkflowHookByTokenHash(r.Context(), hashHookToken(token))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "hook not found")
			return
		}
		slog.Error("workflow hook: token lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Feature flag with the hook's workspace as eval context — while off the
	// route behaves as unregistered (AC6). Checked before the status/429 so a
	// disabled-flag deployment reveals nothing about token validity.
	flagCtx := featureflag.WithEvalContext(r.Context(), featureflag.EvalContext{
		WorkspaceID: uuidToString(hook.WorkspaceID),
	})
	if !h.FeatureFlags.IsEnabled(flagCtx, workflow.FlagEngine, false) {
		writeError(w, http.StatusNotFound, "hook not found")
		return
	}

	if hook.Status != "active" {
		writeError(w, http.StatusUnauthorized, "hook is disabled")
		return
	}

	if h.TokenLimiter != nil {
		if !h.TokenLimiter.Allow(r.Context(), token) {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
			return
		}
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var payload workflowHookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	payload.SourceID = strings.TrimSpace(payload.SourceID)
	payload.Title = strings.TrimSpace(payload.Title)
	if payload.SourceID == "" {
		writeError(w, http.StatusBadRequest, "source_id is required")
		return
	}
	if payload.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	templateID, err := h.resolveHookTemplate(r, hook, payload.TemplateKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	reviewerID, err := h.resolveHookReviewer(r, hook.WorkspaceID, payload.Reviewer)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	description := payload.Description
	if payload.SourceURL != "" {
		if description != "" {
			description += "\n\n"
		}
		description += "Source: " + payload.SourceURL
	}

	run, created, err := h.Engine.StartRun(r.Context(), workflow.StartRunParams{
		WorkspaceID: hook.WorkspaceID,
		TemplateID:  templateID,
		SourceType:  "hook",
		SourceID:    payload.SourceID,
		Title:       payload.Title,
		Description: description,
		ReviewerID:  reviewerID,
	})
	if err != nil {
		writeWorkflowRunStartError(w, err)
		return
	}

	// P0 delivery audit (design.md §1): last_used_at + structured log.
	if err := h.Queries.TouchWorkflowHookLastUsedAt(r.Context(), hook.ID); err != nil {
		slog.Warn("workflow hook: touch last_used_at failed", "hook_id", uuidToString(hook.ID), "error", err)
	}
	slog.Info("workflow hook: inbound delivery",
		"hook_id", uuidToString(hook.ID),
		"run_id", uuidToString(run.ID),
		"source_id", payload.SourceID,
		"template_id", uuidToString(templateID),
		"created", created,
	)

	resp := workflowHookResponse{
		RunID:         uuidToString(run.ID),
		IntakeIssueID: uuidToString(run.IntakeIssueID),
		Created:       created,
	}
	if run.IntakeIssueID.Valid {
		if intake, ierr := h.Queries.GetIssue(r.Context(), run.IntakeIssueID); ierr == nil {
			resp.IssueNumber = intake.Number
		}
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, resp)
}

// resolveHookTemplate picks the run's template: the payload's template_key
// resolved to the newest published version in the hook's workspace, else the
// template bound at hook creation.
func (h *WorkflowHookHandler) resolveHookTemplate(r *http.Request, hook db.WorkflowHook, templateKey string) (pgtype.UUID, error) {
	if strings.TrimSpace(templateKey) == "" {
		return hook.TemplateID, nil
	}
	tmpl, err := h.Queries.GetPublishedWorkflowTemplateByKey(r.Context(), db.GetPublishedWorkflowTemplateByKeyParams{
		WorkspaceID: hook.WorkspaceID,
		Key:         strings.TrimSpace(templateKey),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, fmt.Errorf("template_key %q does not resolve to a published template", templateKey)
		}
		return pgtype.UUID{}, fmt.Errorf("template lookup failed: %w", err)
	}
	return tmpl.ID, nil
}

// resolveHookReviewer maps the payload's reviewer (member id or email) to a
// member of the hook's workspace. Empty reviewer is valid (no designated
// reviewer — the acceptance falls back to the template default).
func (h *WorkflowHookHandler) resolveHookReviewer(r *http.Request, workspaceID pgtype.UUID, reviewer string) (pgtype.UUID, error) {
	reviewer = strings.TrimSpace(reviewer)
	if reviewer == "" {
		return pgtype.UUID{}, nil
	}
	if id, err := util.ParseUUID(reviewer); err == nil {
		member, err := h.Queries.GetMember(r.Context(), id)
		if err == nil && member.WorkspaceID == workspaceID {
			return member.ID, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, fmt.Errorf("reviewer lookup failed: %w", err)
		}
		return pgtype.UUID{}, fmt.Errorf("reviewer %q is not a member of this workspace", reviewer)
	}
	members, err := h.Queries.ListMembersWithUser(r.Context(), workspaceID)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("reviewer lookup failed: %w", err)
	}
	for _, m := range members {
		if strings.EqualFold(m.UserEmail, reviewer) {
			return m.ID, nil
		}
	}
	return pgtype.UUID{}, fmt.Errorf("reviewer %q is not a member of this workspace", reviewer)
}

// writeWorkflowRunStartError maps StartRun failures to hook-ingress codes.
func writeWorkflowRunStartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workflow.ErrTemplateNotFound):
		writeError(w, http.StatusNotFound, "template not found")
	case errors.Is(err, workflow.ErrTemplateNotPublished):
		writeError(w, http.StatusConflict, "bound template is not published")
	default:
		slog.Error("workflow hook: start run failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to start workflow run")
	}
}

// ---------------------------------------------------------------------------
// Management API — POST/GET /api/workflow-hooks, POST /api/workflow-hooks/{id}/disable
// ---------------------------------------------------------------------------

type workflowHookDTO struct {
	ID         string  `json:"id"`
	TemplateID string  `json:"template_id"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

// workflowHookToDTO deliberately omits TokenHash: the list/detail payloads
// must never carry even the hash (it is the offline-crack target for the
// bearer credential).
func workflowHookToDTO(hook db.WorkflowHook) workflowHookDTO {
	dto := workflowHookDTO{
		ID:         uuidToString(hook.ID),
		TemplateID: uuidToString(hook.TemplateID),
		Name:       hook.Name,
		Status:     hook.Status,
		CreatedAt:  timestampToString(hook.CreatedAt),
	}
	if hook.LastUsedAt.Valid {
		s := timestampToString(hook.LastUsedAt)
		dto.LastUsedAt = &s
	}
	return dto
}

type createWorkflowHookRequest struct {
	TemplateID string `json:"template_id"`
	Name       string `json:"name"`
}

type createWorkflowHookResponse struct {
	workflowHookDTO
	// Token is the cleartext bearer credential, returned exactly once.
	Token string `json:"token"`
}

// CreateHook mints a hook bound to a workspace template and returns the
// cleartext token once; only its SHA-256 hash is persisted.
func (h *WorkflowHookHandler) CreateHook(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	var req createWorkflowHookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	templateID, ok := parseUUIDOrBadRequest(w, req.TemplateID, "template_id")
	if !ok {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if _, err := h.Queries.GetWorkflowTemplateInWorkspace(r.Context(), db.GetWorkflowTemplateInWorkspaceParams{
		ID: templateID, WorkspaceID: workspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "template not found in this workspace")
			return
		}
		writeError(w, http.StatusInternalServerError, "template lookup failed")
		return
	}

	token, err := generateHookToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	hook, err := h.Queries.CreateWorkflowHook(r.Context(), db.CreateWorkflowHookParams{
		WorkspaceID: workspaceID,
		TemplateID:  templateID,
		TokenHash:   hashHookToken(token),
		Name:        strings.TrimSpace(req.Name),
	})
	if err != nil {
		slog.Error("workflow hook: create failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create hook")
		return
	}
	writeJSON(w, http.StatusCreated, createWorkflowHookResponse{
		workflowHookDTO: workflowHookToDTO(hook),
		Token:           token,
	})
}

// ListHooks returns the workspace's hooks (newest first), never token hashes.
func (h *WorkflowHookHandler) ListHooks(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	hooks, err := h.Queries.ListWorkflowHooks(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list hooks")
		return
	}
	out := make([]workflowHookDTO, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, workflowHookToDTO(hook))
	}
	writeJSON(w, http.StatusOK, out)
}

// DisableHook flips a hook to status='disabled'; inbound posts then 401.
func (h *WorkflowHookHandler) DisableHook(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, ctxWorkspaceID(r.Context()), "workspace_id")
	if !ok {
		return
	}
	hookID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "id")
	if !ok {
		return
	}
	hook, err := h.Queries.SetWorkflowHookStatus(r.Context(), db.SetWorkflowHookStatusParams{
		ID: hookID, Status: "disabled", WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "hook not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to disable hook")
		return
	}
	writeJSON(w, http.StatusOK, workflowHookToDTO(hook))
}
