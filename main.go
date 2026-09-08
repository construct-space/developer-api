package main

import (
	"log"
	"net/http"
	"time"

	"construct/dev-portal/internal/config"
	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/handlers"
	"construct/dev-portal/internal/middleware"
	"construct/dev-portal/internal/models"
	"construct/dev-portal/internal/seed"
)

func main() {
	cfg := config.Load()
	handlers.Cfg = cfg

	database.Init(cfg)
	if err := database.DB.AutoMigrate(&models.Space{}, &models.Publisher{}, &models.Session{}, &models.CLIToken{}, &models.SpaceTransfer{}, &models.SpacePublish{}); err != nil {
		log.Fatalf("auto-migrate: %v", err)
	}

	// One-shot: drop the obsolete unique index on publishers.email. A user's
	// personal publisher and their org's publisher legitimately share the
	// user's email; name + api_key remain unique. Ignoring the error covers
	// the "already dropped" case on subsequent boots.
	if database.DB.Migrator().HasIndex(&models.Publisher{}, "idx_publishers_email") {
		if err := database.DB.Migrator().DropIndex(&models.Publisher{}, "idx_publishers_email"); err != nil {
			log.Printf("[migrate] drop idx_publishers_email: %v", err)
		}
	}

	// One-shot: backfill spaces.scopes_json + spaces.project_aware from the
	// legacy single `scope` column, then drop it. Mapping mirrors the
	// frontend's old normaliser:
	//   "app"               → scopes:["app"], projectAware:false
	//   "org" / "company"   → scopes:["org"], projectAware:false
	//   "project"           → scopes:["app"], projectAware:true
	//   "both"              → scopes:["app","org"], projectAware:true
	//   anything else / ''  → scopes:["app"], projectAware:false
	// Idempotent — gated on whether the old column still exists.
	if database.DB.Migrator().HasColumn(&models.Space{}, "scope") {
		mappings := []struct {
			match   string
			scopes  string
			project bool
		}{
			{"app", `["app"]`, false},
			{"org", `["org"]`, false},
			{"company", `["org"]`, false},
			{"project", `["app"]`, true},
			{"both", `["app","org"]`, true},
		}
		for _, m := range mappings {
			if err := database.DB.Exec(
				"UPDATE spaces SET scopes_json = ?, project_aware = ? WHERE scope = ? AND (scopes_json IS NULL OR scopes_json = '' OR scopes_json = '[\"app\"]')",
				m.scopes, m.project, m.match,
			).Error; err != nil {
				log.Printf("[migrate] backfill scopes for scope=%q: %v", m.match, err)
			}
		}
		// Drop the legacy column once the data has been copied. Errors here
		// are logged but non-fatal; a follow-up boot will retry.
		if err := database.DB.Migrator().DropColumn(&models.Space{}, "scope"); err != nil {
			log.Printf("[migrate] drop spaces.scope: %v", err)
		}
	}

	seed.Run()

	mux := http.NewServeMux()

	// Auth routes (OAuth with Construct Accounts)
	mux.HandleFunc("GET /api/auth/login", handlers.AuthLogin)
	mux.HandleFunc("GET /api/auth/callback", handlers.AuthCallback)
	mux.HandleFunc("GET /api/auth/me", handlers.AuthMe)
	mux.HandleFunc("POST /api/auth/logout", handlers.AuthLogout)
	mux.HandleFunc("GET /api/auth/logout", handlers.AuthLogout)
	mux.HandleFunc("GET /api/auth/sessions", handlers.AuthSessions)
	mux.HandleFunc("DELETE /api/auth/sessions/{id}", handlers.AuthDeleteSession)

	// Key verification (called by PaaS to validate publisher keys)
	mux.HandleFunc("GET /api/auth/verify-key", handlers.VerifyKey)

	// CLI auth routes
	mux.HandleFunc("GET /api/auth/cli-login", handlers.CLILogin)
	mux.HandleFunc("GET /api/auth/callback-cli", handlers.CLICallback)
	mux.HandleFunc("GET /api/auth/cli-verify", handlers.CLIVerify)
	mux.HandleFunc("POST /api/auth/cli-revoke", handlers.CLIRevoke)

	// CLI publish route
	mux.HandleFunc("POST /api/publish", handlers.PublishSpace)

	// Bundle downloads
	mux.HandleFunc("GET /api/downloads/{name}/{version}/bundle.tar.gz", handlers.DownloadBundle)

	// Admin routes
	mux.HandleFunc("GET /api/admin/spaces", handlers.AdminListSpaces)
	mux.HandleFunc("POST /api/admin/spaces/{name}/review", handlers.AdminReviewSpace)

	// Admin: publisher lookup and verify/unverify
	mux.HandleFunc("GET /api/admin/publishers/by-user/{user_id}", handlers.AdminGetPublisherByUser)
	mux.HandleFunc("PUT /api/admin/publishers/{id}/verify", handlers.AdminVerifyPublisher)
	mux.HandleFunc("PUT /api/admin/publishers/{id}/unverify", handlers.AdminUnverifyPublisher)

	// Admin: pending-spaces queue and per-space actions
	mux.HandleFunc("GET /api/admin/spaces/pending-review", handlers.AdminListPendingSpaces)
	mux.HandleFunc("GET /api/admin/spaces/all", handlers.AdminListAllSpaces)
	mux.HandleFunc("GET /api/admin/spaces/{id}", handlers.AdminGetSpace)
	mux.HandleFunc("PUT /api/admin/spaces/{id}", handlers.AdminUpdateSpace)
	mux.HandleFunc("DELETE /api/admin/spaces/{id}", handlers.AdminDeleteSpace)
	mux.HandleFunc("POST /api/admin/spaces/{id}/approve", handlers.AdminApproveSpace)
	mux.HandleFunc("POST /api/admin/spaces/{id}/reject", handlers.AdminRejectSpace)
	mux.HandleFunc("POST /api/admin/spaces/{id}/request-changes", handlers.AdminRequestChangesSpace)
	mux.HandleFunc("POST /api/admin/spaces/{id}/unpublish", handlers.AdminUnpublishSpace)
	mux.HandleFunc("POST /api/admin/spaces/{id}/toggle-recommended", handlers.AdminToggleRecommended)
	mux.HandleFunc("POST /api/admin/spaces/{id}/transfer", handlers.AdminTransferSpace)

	// Admin: publish history + per-version source download (Oracle review)
	mux.HandleFunc("GET /api/admin/spaces/{id}/publishes", handlers.AdminListSpacePublishes)
	mux.HandleFunc("GET /api/admin/spaces/{id}/source", handlers.AdminDownloadLatestSource)
	mux.HandleFunc("GET /api/admin/publishes/{id}/source", handlers.AdminDownloadPublishSource)
	mux.HandleFunc("POST /api/admin/spaces/status", handlers.AdminBatchSpaceStatus)

	// Admin: publisher list and detail (proxied by oracle)
	mux.HandleFunc("GET /api/admin/publishers", handlers.AdminListPublishers)
	mux.HandleFunc("GET /api/admin/publishers/{id}", handlers.AdminGetPublisher)

	// Spaces API
	mux.HandleFunc("GET /api/registry", handlers.Registry)
	mux.HandleFunc("GET /api/categories", handlers.ListCategories)
	mux.HandleFunc("GET /api/spaces", handlers.ListSpaces)
	mux.HandleFunc("GET /api/spaces/mine", handlers.ListMySpaces)
	mux.HandleFunc("GET /api/spaces/{name}", handlers.GetSpace)
	mux.HandleFunc("POST /api/spaces", handlers.CreateSpace)
	mux.HandleFunc("PUT /api/spaces/{name}", handlers.UpdateSpace)
	mux.HandleFunc("DELETE /api/spaces/{name}", handlers.DeleteSpace)
	mux.HandleFunc("POST /api/spaces/{name}/fetch-manifest", handlers.FetchManifest)
	mux.HandleFunc("POST /api/spaces/{name}/submit", handlers.SubmitSpace)
	mux.HandleFunc("GET /api/spaces/{name}/ownership", handlers.GetSpaceOwnership)
	mux.HandleFunc("GET /api/spaces/{name}/publishes", handlers.ListSpacePublishes)
	mux.HandleFunc("POST /api/spaces/{name}/transfer", handlers.TransferOwnership) // legacy one-shot — kept during cutover

	// Space ownership transfer — two-sided handshake
	mux.HandleFunc("POST /api/spaces/{name}/transfers", handlers.CreateTransfer)
	mux.HandleFunc("GET /api/transfers/incoming", handlers.ListIncomingTransfers)
	mux.HandleFunc("GET /api/transfers/outgoing", handlers.ListOutgoingTransfers)
	mux.HandleFunc("POST /api/transfers/{id}/accept", handlers.AcceptTransfer)
	mux.HandleFunc("POST /api/transfers/{id}/decline", handlers.DeclineTransfer)
	mux.HandleFunc("DELETE /api/transfers/{id}", handlers.CancelTransfer)

	// Publishers API
	mux.HandleFunc("POST /api/register", handlers.RegisterPublisher)
	mux.HandleFunc("GET /api/publisher", handlers.GetMyPublisher)
	mux.HandleFunc("GET /api/publisher/org", handlers.GetOrgPublisher)
	mux.HandleFunc("PUT /api/publisher", handlers.UpdateMyPublisher)
	mux.HandleFunc("GET /api/publishers/{name}", handlers.GetPublisher)
	mux.HandleFunc("POST /api/publishers/regenerate-key", handlers.RegenerateKey)

	// Enrollment — personal developer and org-as-publisher
	mux.HandleFunc("POST /api/enroll/personal", handlers.EnrollPersonal)
	mux.HandleFunc("POST /api/enroll/org", handlers.EnrollOrg)
	mux.HandleFunc("DELETE /api/enroll/personal", handlers.UnenrollPersonal)
	mux.HandleFunc("DELETE /api/enroll/org", handlers.UnenrollOrg)

	// Data API (proxy to PaaS)
	mux.HandleFunc("GET /api/data/schemas", handlers.ListDataSchemas)
	mux.HandleFunc("GET /api/data/stats", handlers.DataStats)
	mux.HandleFunc("POST /api/data/graphql", handlers.DataGraphQL)

	// Graph façade — public admin entry point for schemas. CLI's `construct
	// graph push` and my's developer UI call these; developer proxies to
	// srv-captain--graph internally so graph.lisaos.dev can stay pure
	// GraphQL runtime. Identity forwarded via X-Auth-* + X-Internal-Secret.
	mux.HandleFunc("POST /api/schemas/register", handlers.GraphSchemaRegister)
	mux.HandleFunc("GET /api/schemas/{spaceId}", handlers.GraphSchemaGet)
	mux.HandleFunc("DELETE /api/schemas/{spaceId}", handlers.GraphSchemaDelete)
	mux.HandleFunc("GET /api/schemas/{spaceId}/tables/{tableName}/rows", handlers.GraphTableRows)

	// Internal service-to-service endpoints (gated by X-Internal-Secret header)
	mux.HandleFunc("GET /internal/publisher", handlers.InternalGetPublisherByUser)
	mux.HandleFunc("GET /internal/publisher/org", handlers.InternalGetPublisherByOrg)
	mux.HandleFunc("POST /internal/publishers/backfill", handlers.InternalBackfillPublishers)
	mux.HandleFunc("POST /internal/spaces/archive-for-org", handlers.InternalArchiveSpacesForOrg)

	// Marketplace bootstrap — bulk-promote approved spaces into the
	// marketplace catalog. Path lives under /api/admin so the gateway
	// (my.lisaos.dev) proxies it via /api/developer/admin/...
	// while requireInternalSecret keeps it staff-only.
	mux.HandleFunc("POST /api/admin/marketplace/backfill", handlers.AdminBackfillMarketplace)

	// Health check
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		handlers.WriteJSON(w, 200, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		handlers.WriteJSON(w, 200, map[string]any{"status": "ok"})
	})

	// Apply middleware
	var handler http.Handler = mux
	handler = middleware.CORS(cfg)(handler)
	handler = middleware.SecurityHeaders(handler)
	handler = middleware.RateLimit(handler)
	handler = middleware.Logger(handler)

	log.Printf("Construct Spaces Portal running on :%s", cfg.Port)
	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}
