package handlers

import (
	"io"
	"net/http"
	"os"
	"strings"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
)

// graph_facade — developer acts as the single admin entry point for graph.
// Clients (CLI, my's developer UI) call /api/schemas/* on this service via
// my's gateway; we proxy those calls to srv-captain--graph on the Swarm
// overlay with an internal-secret handshake so graph doesn't have to expose
// admin endpoints publicly.
//
// The public graph surface collapses to just POST /graphql (runtime). Every
// other operation humans or CLIs do on a schema — register, fetch, drop,
// eventually migrate + stats + data-browse — comes in through here.

const defaultGraphInternalURL = "http://srv-captain--graph"

// proxyToGraph is the shared transport: take this request, forward it to
// graph at the given path, preserve method + body + query, and write graph's
// response back to the caller. Identity flows as X-Auth-* headers signed by
// X-Internal-Secret so graph's auth middleware (see go-shared/auth.Gateway)
// trusts it without re-validating the user's token.
func proxyToGraph(w http.ResponseWriter, r *http.Request, targetPath string, decorate ...func(*http.Request)) {
	uid := currentUserID(r)
	if uid == "" {
		WriteJSON(w, 401, map[string]any{"error": "unauthorized"})
		return
	}

	secret := os.Getenv("INTERNAL_SHARED_SECRET")
	if secret == "" {
		WriteJSON(w, 503, map[string]any{"error": "graph facade not configured (missing INTERNAL_SHARED_SECRET)"})
		return
	}

	base := os.Getenv("GRAPH_INTERNAL_URL")
	if base == "" {
		base = defaultGraphInternalURL
	}
	url := base + targetPath
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, r.Body)
	if err != nil {
		WriteJSON(w, 500, map[string]any{"error": err.Error()})
		return
	}

	// Identity headers. Prefer values already asserted by my's gateway;
	// otherwise synthesize from the session/CLI-token path — graph only
	// needs the user UUID to make its own authorization decisions.
	req.Header.Set("X-Internal-Secret", secret)
	req.Header.Set("X-Auth-User-ID", uid)
	if id := gatewayIdentity(r); id != nil {
		setIfNonEmpty(req.Header, "X-Auth-User-Email", id.Email)
		setIfNonEmpty(req.Header, "X-Auth-User-Name", id.Name)
		setIfNonEmpty(req.Header, "X-Auth-Scope", id.Scope)
		setIfNonEmpty(req.Header, "X-Auth-Org-ID", id.OrgID)
		if len(id.Roles) > 0 {
			req.Header.Set("X-Auth-Roles", strings.Join(id.Roles, ","))
		}
	} else if t := getCLIToken(r); t != nil {
		setIfNonEmpty(req.Header, "X-Auth-User-Email", t.Email)
		setIfNonEmpty(req.Header, "X-Auth-User-Name", t.Name)
	}

	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if rid := r.Header.Get("X-Request-ID"); rid != "" {
		req.Header.Set("X-Request-ID", rid)
	}

	for _, fn := range decorate {
		fn(req)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		WriteJSON(w, 502, map[string]any{"error": "graph unreachable: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	// Pass through the headers that actually matter; everything else is
	// noise (Server, X-Powered-By, internal correlation headers).
	for _, h := range []string{"Content-Type", "Content-Length", "Cache-Control", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func setIfNonEmpty(h http.Header, key, value string) {
	if value != "" {
		h.Set(key, value)
	}
}

// POST /api/schemas/register
// Body: { space_id, space_name, models[], version? }
// Called by `construct graph push` and by my's developer UI schema editor.
func GraphSchemaRegister(w http.ResponseWriter, r *http.Request) {
	proxyToGraph(w, r, "/api/schemas/register")
}

// GET /api/schemas/{spaceId}
// Returns the registered schema for a space (types + fields + indexes).
func GraphSchemaGet(w http.ResponseWriter, r *http.Request) {
	spaceID := r.PathValue("spaceId")
	if spaceID == "" {
		WriteJSON(w, 400, map[string]any{"error": "spaceId required"})
		return
	}
	proxyToGraph(w, r, "/api/schemas/"+spaceID)
}

// DELETE /api/schemas/{spaceId}
// Drops a schema + all its data.
func GraphSchemaDelete(w http.ResponseWriter, r *http.Request) {
	spaceID := r.PathValue("spaceId")
	if spaceID == "" {
		WriteJSON(w, 400, map[string]any{"error": "spaceId required"})
		return
	}
	proxyToGraph(w, r, "/api/schemas/"+spaceID)
}

// GET /api/schemas/{spaceId}/tables/{tableName}/rows
// Owner-scoped data browse for a space's table. Developer is the source of
// truth for space ownership (its `spaces` table tracks submitted_by + the
// transferable owner_user_id / owner_org_id) — graph denormalizes a copy
// in `_system.spaces.owner_user_id` but it's frequently NULL for spaces
// registered before that column existed. We verify here against developer's
// own DB and pass `X-Internal-Owner-Verified: true` so graph can trust the
// caller without re-checking its potentially-stale denormalization.
func GraphTableRows(w http.ResponseWriter, r *http.Request) {
	spaceID := r.PathValue("spaceId")
	tableName := r.PathValue("tableName")
	if spaceID == "" || tableName == "" {
		WriteJSON(w, 400, map[string]any{"error": "spaceId and tableName required"})
		return
	}

	if verifySpaceOwnership(r, spaceID) {
		proxyToGraph(w, r, "/api/spaces/"+spaceID+"/tables/"+tableName+"/rows",
			func(req *http.Request) { req.Header.Set("X-Internal-Owner-Verified", "true") })
		return
	}
	proxyToGraph(w, r, "/api/spaces/"+spaceID+"/tables/"+tableName+"/rows")
}

// verifySpaceOwnership checks developer's own spaces table against the
// caller's identity. Returns true when:
//   - personal owner: spaces.owner_user_id == caller.UserID, or
//   - legacy owner: owner_user_id is empty AND submitted_by == caller.UserID, or
//   - org owner: spaces.owner_org_id == caller.OrgID with publisher-management
//     authority for that org.
//
// Never trusts an inbound `X-Internal-Owner-Verified` header — that would
// let any client fake ownership. The flag is only set by us, on the
// outbound proxy request, after this check passes.
func verifySpaceOwnership(r *http.Request, spaceID string) bool {
	caller := resolveCaller(r)
	if caller == nil || caller.UserID == "" {
		return false
	}
	var space models.Space
	if err := database.DB.Where("name = ?", spaceID).First(&space).Error; err != nil {
		return false
	}
	if space.OwnerUserID != nil && *space.OwnerUserID != "" && *space.OwnerUserID == caller.UserID {
		return true
	}
	if (space.OwnerUserID == nil || *space.OwnerUserID == "") &&
		space.SubmittedBy != nil && *space.SubmittedBy == caller.UserID {
		return true
	}
	if space.OwnerOrgID != nil && *space.OwnerOrgID != "" &&
		caller.OrgID == *space.OwnerOrgID && caller.CanManageOrgPublisher {
		return true
	}
	return false
}
