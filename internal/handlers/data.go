package handlers

import (
	"io"
	"net/http"
	"strings"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
)

// GET /api/auth/verify-key — PaaS calls this to validate publisher API keys
func VerifyKey(w http.ResponseWriter, r *http.Request) {
	apiKey := r.Header.Get("X-API-Key")
	if apiKey == "" {
		auth := r.Header.Get("Authorization")
		apiKey = strings.TrimPrefix(auth, "Bearer ")
		if apiKey == auth {
			apiKey = ""
		}
	}
	if apiKey == "" {
		WriteJSON(w, 401, map[string]any{"error": "API key required"})
		return
	}

	var publisher models.Publisher
	if err := database.DB.Where("api_key = ?", apiKey).First(&publisher).Error; err != nil {
		WriteJSON(w, 401, map[string]any{"error": "Invalid API key"})
		return
	}

	resp := map[string]any{
		"valid":    true,
		"name":     publisher.Name,
		"email":    publisher.Email,
		"verified": publisher.Verified,
	}
	// Identity anchor — exactly one of user_id / org_id is non-empty. Graph
	// reads these to attribute the request to a publisher's owning identity
	// (sets X-Auth-Org-ID / X-Auth-User-ID) so downstream ownership checks
	// in the schema registry treat publisher-key calls as org-scoped pushes.
	if publisher.UserID != nil && *publisher.UserID != "" {
		resp["user_id"] = *publisher.UserID
		resp["kind"] = "user"
	}
	if publisher.OrgID != nil && *publisher.OrgID != "" {
		resp["org_id"] = *publisher.OrgID
		resp["kind"] = "org"
	}
	WriteJSON(w, 200, resp)
}

// GET /author/data — Data dashboard page
func AuthorDataPage(w http.ResponseWriter, r *http.Request) {
	session := requireSession(w, r)
	if session == nil {
		return
	}

	user := sessionUser(session)
	spaces := mySpaces(*session.UserID)

	var publisher *models.Publisher
	if session.Email != nil {
		var p models.Publisher
		if err := database.DB.Where("email = ?", *session.Email).First(&p).Error; err == nil {
			publisher = &p
		}
	}

	data := map[string]any{
		"User":      user,
		"Spaces":    spaces,
		"Publisher": publisher,
		"PaasURL":   paasURL(),
	}
	renderPage(w, "author/data.html", data)
}

// GET /api/data/schemas — list provisioned schemas for this developer
func ListDataSchemas(w http.ResponseWriter, r *http.Request) {
	if !dataAuthOK(r) {
		WriteJSON(w, 401, map[string]any{"error": "Unauthorized"})
		return
	}

	resp, err := paasRequest("GET", "/api/admin/schemas", nil, r)
	if err != nil {
		WriteJSON(w, 502, map[string]any{"error": "PaaS unreachable"})
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// GET /api/data/stats — PaaS stats for this developer
func DataStats(w http.ResponseWriter, r *http.Request) {
	if !dataAuthOK(r) {
		WriteJSON(w, 401, map[string]any{"error": "Unauthorized"})
		return
	}

	resp, err := paasRequest("GET", "/api/admin/stats", nil, r)
	if err != nil {
		WriteJSON(w, 502, map[string]any{"error": "PaaS unreachable"})
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// POST /api/data/graphql — proxy GraphQL requests to PaaS with auth
func DataGraphQL(w http.ResponseWriter, r *http.Request) {
	if !dataAuthOK(r) {
		WriteJSON(w, 401, map[string]any{"error": "Unauthorized"})
		return
	}

	resp, err := paasRequest("POST", "/graphql", r.Body, r)
	if err != nil {
		WriteJSON(w, 502, map[string]any{"error": "PaaS unreachable"})
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// dataAuthOK returns true when the request carries a valid identity, from
// any of three sources:
//   1. Gateway-asserted X-Auth-* headers (my.lisaos.dev proxy path —
//      token already validated at the gateway, PRD §4).
//   2. Session cookie (web UI direct).
//   3. Bearer token (CLI, Construct app, third-party). getCLIToken handles
//      the three Bearer shapes: cst_live_…, cat_…, csk_live_…
//
// The gateway path is tried first — when the request came through
// my.lisaos.dev/api/developer/*, the identity is already decoded into
// headers and there's no reason to do additional DB lookups.
func dataAuthOK(r *http.Request) bool {
	if gatewayIdentity(r) != nil {
		return true
	}
	if getSession(r) != nil {
		return true
	}
	return getCLIToken(r) != nil
}

// GET /author/data/playground — GraphQL playground proxied through dev-portal
func AuthorPlaygroundPage(w http.ResponseWriter, r *http.Request) {
	session := requireSession(w, r)
	if session == nil {
		return
	}
	renderPage(w, "author/playground.html", map[string]any{
		"User": sessionUser(session),
	})
}

func paasURL() string {
	if Cfg != nil && Cfg.PaasURL != "" {
		return Cfg.PaasURL
	}
	return "https://paas.construct.ninja"
}

func paasRequest(method, path string, body io.Reader, original *http.Request) (*http.Response, error) {
	url := paasURL() + path
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}

	// Authorization resolution order:
	//   1. Web session → forward the session's AccessToken (existing path)
	//   2. Bearer Authorization header on the original request → forward
	//      verbatim. Covers Space Developer in the desktop app (accounts
	//      OAuth token, cat_…) and CLI tokens (cst_live_…). PaaS owns final
	//      validation.
	if session := getSession(original); session != nil && session.AccessToken != nil {
		req.Header.Set("Authorization", "Bearer "+*session.AccessToken)
	} else if auth := original.Header.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Content-Type", "application/json")
	// Forward space/project context
	if v := original.Header.Get("X-Space-ID"); v != "" {
		req.Header.Set("X-Space-ID", v)
	}
	if v := original.Header.Get("X-Project-ID"); v != "" {
		req.Header.Set("X-Project-ID", v)
	}
	return http.DefaultClient.Do(req)
}
