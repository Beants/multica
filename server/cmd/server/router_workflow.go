package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/workflow"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// router_workflow.go — workflow-engine route registration (P0 fork file).
// The single upstream touch point is the registerWorkflowRoutes call in
// router.go; everything else lives here.

// registerWorkflowRoutes wires every workflow-engine route. It is called at
// the ROOT mux (not inside the authenticated group) because the inbound hook
// is a public ingress — the bearer token in the URL path IS the credential,
// mirroring the autopilot webhook (handler/autopilot_webhook.go). authMW is
// the upstream-constructed Auth middleware (it closes over the PAT cache and
// cloud-PAT verifier, neither of which is reachable from the Handler struct),
// reused verbatim for the authenticated subgroup so session/PAT/mat_ handling
// stays byte-identical to upstream.
//
// Route families:
//   - PUBLIC:  POST /api/hooks/workflow/{token} — inbound hook (flag-gated
//     inside the handler, where the hook's workspace is known).
//   - authed:  /api/tasks/{id}/{submission,verdict,step-context} (mat_ only),
//     /api/workflow-templates*, /api/workflow-hooks*, /api/workflow-runs*
//     (session/PAT). All gated by the workflow_engine flag — while off they
//     answer 404, so the flag-disabled deployment is behaviorally identical
//     to not registering at all (AC6).
func registerWorkflowRoutes(r chi.Router, h *handler.Handler, authMW func(http.Handler) http.Handler) {
	engine := workflow.NewEngine(h.Queries, h.TxStarter, h.IssueService, h.TaskService, h.Bus)
	templates := workflow.NewTemplateService(h.Queries, h.TxStarter)
	wh := handler.NewWorkflowHandler(h.Queries, engine)
	wth := handler.NewWorkflowTemplateHandler(h.Queries, templates)
	wrh := handler.NewWorkflowRunHandler(h.Queries, engine)
	hookH := handler.NewWorkflowHookHandler(
		h.Queries, engine, h.FeatureFlags,
		h.WebhookIPRateLimiter, h.WebhookRateLimiter,
		h.ClientIPForRateLimit,
	)

	// Public ingress — outside any auth middleware by design. Rate limiting
	// (per-IP then per-token) happens in the handler before any DB I/O.
	r.Post("/api/hooks/workflow/{token}", hookH.HandleInboundHook)

	r.Group(func(r chi.Router) {
		r.Use(authMW)
		r.Use(middleware.RequireWorkspaceMember(h.Queries))
		r.Use(workflowEngineGate(h.FeatureFlags))

		// Agent-facing (mat_ task token only; human callers get 403).
		r.Route("/api/tasks/{id}", func(r chi.Router) {
			r.Post("/submission", wh.CreateSubmission)
			r.Post("/verdict", wh.CreateVerdict)
			r.Get("/verdict", wh.GetVerdict)
			r.Get("/step-context", wh.GetStepContext)
		})

		// Template management (draft CRUD + publish/archive lifecycle).
		// RequireHumanActor: a mat_ token's stamped X-User-ID is the runtime
		// OWNER's user id, so without the gate an agent would pass
		// RequireWorkspaceMember as its owner and reach these human surfaces
		// (design.md §3: templates/hooks/runs are session/PAT-only).
		r.Route("/api/workflow-templates", func(r chi.Router) {
			r.Use(handler.RequireHumanActor)
			r.Post("/", wth.CreateTemplate)
			r.Get("/", wth.ListTemplates)
			// Static segment: chi prefers it over /{id} for POST /seed.
			r.Post("/seed", wth.SeedTemplates)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", wth.GetTemplate)
				r.Put("/", wth.UpdateTemplate)
				r.Post("/publish", wth.PublishTemplate)
				r.Post("/archive", wth.ArchiveTemplate)
			})
		})

		// Hook management (create returns the cleartext token exactly once).
		r.Route("/api/workflow-hooks", func(r chi.Router) {
			r.Use(handler.RequireHumanActor)
			r.Post("/", hookH.CreateHook)
			r.Get("/", hookH.ListHooks)
			r.Post("/{id}/disable", hookH.DisableHook)
		})

		// Run inspection + acceptance decisions. The acceptance endpoints are
		// the control plane the verdict actor model exists to protect — an
		// executor agent must never approve its own work.
		r.Route("/api/workflow-runs", func(r chi.Router) {
			r.Use(handler.RequireHumanActor)
			r.Get("/", wrh.ListRuns)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", wrh.GetRun)
				r.Get("/diagnosis", wrh.GetRunDiagnosis)
				r.Post("/acceptance/approve", wrh.ApproveAcceptance)
				r.Post("/acceptance/reject", wrh.RejectAcceptance)
			})
		})

		// P1-4 Rules asset CRUD (design.md §2 支柱 5). Operator surface —
		// every route sits behind RequireHumanActor (team governance, not
		// agent self-service).
		ruleH := handler.NewWorkflowRuleHandler(h.Queries)
		r.Route("/api/workflow-rules", func(r chi.Router) {
			r.Use(handler.RequireHumanActor)
			r.Post("/", ruleH.CreateRule)
			r.Get("/", ruleH.ListRules)
			r.Route("/{id}", func(r chi.Router) {
				r.Delete("/", ruleH.DeleteRule)
				r.Post("/bindings", ruleH.CreateBinding)
				r.Get("/bindings", ruleH.ListBindings)
				r.Delete("/bindings/{bindingId}", ruleH.DeleteBinding)
			})
		})

		// P2-1: event_store read API (dashboard feed + operator queries).
		// workspace-scoped via ctxWorkspaceID; RequireHumanActor (operator surface).
		r.Route("/api/workflow-events", func(r chi.Router) {
			r.Use(handler.RequireHumanActor)
			r.Get("/", h.ListWorkflowEvents)
		})

		// P2-3: aggregated metrics (event_type distribution) for the dashboard.
		r.Route("/api/workflow-metrics", func(r chi.Router) {
			r.Use(handler.RequireHumanActor)
			r.Get("/", h.ListWorkflowMetrics)
		})

		// P2-5: knowledge sediment pool (candidates → extract to Rules).
		r.Route("/api/knowledge-candidates", func(r chi.Router) {
			r.Use(handler.RequireHumanActor)
			r.Post("/", h.CreateKnowledgeCandidate)
			r.Get("/", h.ListKnowledgeCandidates)
			r.Route("/{id}", func(r chi.Router) {
				r.Post("/extract", h.ExtractKnowledgeCandidateToRule)
				r.Delete("/", h.DeleteKnowledgeCandidate)
			})
		})

		// P2-2: outbound webhook config CRUD (external event subscriptions).
		r.Route("/api/workflow-webhooks", func(r chi.Router) {
			r.Use(handler.RequireHumanActor)
			r.Post("/", h.CreateOutboundWebhook)
			r.Get("/", h.ListOutboundWebhooks)
			r.Delete("/{id}", h.DeleteOutboundWebhook)
		})
	})
}

// workflowEngineGate 404s workflow routes while the flag is off. The
// workspace resolved by RequireWorkspaceMember feeds the eval context so
// workspace-scoped rollouts work (design.md §5).
func workflowEngineGate(flags *featureflag.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := featureflag.WithEvalContext(r.Context(), featureflag.EvalContext{
				WorkspaceID: middleware.WorkspaceIDFromContext(r.Context()),
			})
			if !flags.IsEnabled(ctx, workflow.FlagEngine, false) {
				writeNotFound(w)
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func writeNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"not found"}`))
}
