package handlers

import (
	"net/http"
	"strings"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
)

// POST /api/register — create publisher account
func RegisterPublisher(w http.ResponseWriter, r *http.Request) {
	body, err := parseBody(r)
	if err != nil {
		WriteJSON(w, 400, map[string]any{"error": "Body required"})
		return
	}

	name, _ := body["name"].(string)
	email, _ := body["email"].(string)

	if name == "" {
		WriteJSON(w, 400, map[string]any{"error": "name is required"})
		return
	}
	if email == "" {
		WriteJSON(w, 400, map[string]any{"error": "email is required"})
		return
	}

	// Check uniqueness
	var existing models.Publisher
	if err := database.DB.Where("name = ?", name).First(&existing).Error; err == nil {
		WriteJSON(w, 409, map[string]any{"error": "Publisher name already taken"})
		return
	}
	if err := database.DB.Where("email = ?", email).First(&existing).Error; err == nil {
		WriteJSON(w, 409, map[string]any{"error": "Email already registered"})
		return
	}

	apiKey := models.GenerateAPIKey()
	website, _ := body["website"].(string)

	publisher := models.Publisher{
		Name:    name,
		Email:   email,
		APIKey:  apiKey,
		Website: strPtr(website),
	}
	database.DB.Create(&publisher)

	WriteJSON(w, 201, map[string]any{
		"publisher": map[string]any{
			"name":      publisher.Name,
			"email":     publisher.Email,
			"website":   publisher.Website,
			"verified":  publisher.Verified,
			"createdAt": publisher.CreatedAt,
		},
		"apiKey":  apiKey,
		"message": "Save your API key — it will not be shown again.",
	})
}

// GET /api/publishers/{name} — public publisher profile
func GetPublisher(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		WriteJSON(w, 400, map[string]any{"error": "Name required"})
		return
	}

	var publisher models.Publisher
	if err := database.DB.Where("name = ?", name).First(&publisher).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Not found"})
		return
	}

	WriteJSON(w, 200, map[string]any{"publisher": publisher.ToPublicJSON()})
}

// GET /api/publisher — return the authenticated user's personal Publisher.
// Used by the my.lisaos.dev Profile page. Returns { publisher: null }
// (not 404) when the user hasn't enrolled yet so the frontend can render an
// empty-state without treating it as an error.
func GetMyPublisher(w http.ResponseWriter, r *http.Request) {
	uid := currentUserID(r)
	if uid == "" {
		WriteJSON(w, 401, map[string]any{"error": "unauthorized"})
		return
	}

	var p models.Publisher
	err := database.DB.Where("user_id = ? AND user_id <> ''", uid).First(&p).Error
	if err != nil {
		WriteJSON(w, 200, map[string]any{"publisher": nil})
		return
	}

	WriteJSON(w, 200, map[string]any{"publisher": publisherResponse(&p)})
}

// GET /api/publisher/org — return the active org's Publisher, including
// API key, for callers who can manage that org publisher.
func GetOrgPublisher(w http.ResponseWriter, r *http.Request) {
	caller := resolveCaller(r)
	if caller == nil {
		WriteJSON(w, 401, map[string]any{"error": "unauthorized"})
		return
	}
	if caller.OrgID == "" || !caller.CanManageOrgPublisher {
		WriteJSON(w, 403, map[string]any{"error": "only the organization's owner, admin, or developer can view the org publisher key"})
		return
	}

	var p models.Publisher
	if err := database.DB.Where("org_id = ?", caller.OrgID).First(&p).Error; err != nil {
		WriteJSON(w, 200, map[string]any{"publisher": nil})
		return
	}

	resp := publisherResponse(&p)
	resp["api_key"] = p.APIKey
	WriteJSON(w, 200, map[string]any{"publisher": resp})
}

// PUT /api/publisher — update the authenticated user's personal Publisher.
// Body may contain name, slug, description, website. `slug` (if provided)
// is validated with isValidSlug and overrides `name` — both map onto the
// Publisher.Name column, which doubles as the slug in URLs. `description`
// is accepted in the request body but not persisted — the current schema
// has no description column; add one via migration before wiring it up.
func UpdateMyPublisher(w http.ResponseWriter, r *http.Request) {
	uid := currentUserID(r)
	if uid == "" {
		WriteJSON(w, 401, map[string]any{"error": "unauthorized"})
		return
	}

	var p models.Publisher
	if err := database.DB.Where("user_id = ? AND user_id <> ''", uid).First(&p).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "publisher not found — enroll first"})
		return
	}

	body, err := parseBody(r)
	if err != nil {
		WriteJSON(w, 400, map[string]any{"error": "invalid body"})
		return
	}

	updates := map[string]any{}

	// `slug` takes precedence — it's the semantic field. Fall back to `name`
	// for clients that send only one. Both feed Publisher.Name.
	newSlug, hasSlug := body["slug"].(string)
	if !hasSlug || newSlug == "" {
		if n, ok := body["name"].(string); ok && n != "" {
			newSlug = strings.TrimSpace(n)
			hasSlug = true
		}
	} else {
		newSlug = strings.TrimSpace(newSlug)
	}
	if hasSlug && newSlug != "" && newSlug != p.Name {
		if !isValidSlug(newSlug) {
			WriteJSON(w, 400, map[string]any{"error": "slug must be lowercase alphanumeric with dashes only (2–50 chars, no leading/trailing dash)"})
			return
		}
		// Uniqueness — skip if unchanged, otherwise reject collisions.
		var collision models.Publisher
		if err := database.DB.Where("name = ? AND id <> ?", newSlug, p.ID).First(&collision).Error; err == nil {
			WriteJSON(w, 409, map[string]any{"error": "slug already taken"})
			return
		}
		updates["name"] = newSlug
	}

	if website, ok := body["website"].(string); ok {
		updates["website"] = strPtr(strings.TrimSpace(website))
	}

	// `description` accepted in body for forward compatibility but silently
	// dropped — Publisher has no description column yet.

	if len(updates) > 0 {
		if err := database.DB.Model(&p).Updates(updates).Error; err != nil {
			WriteJSON(w, 500, map[string]any{"error": "update failed"})
			return
		}
		// Re-read to return the fresh row (GORM Updates doesn't repopulate).
		database.DB.Where("id = ?", p.ID).First(&p)
	}

	WriteJSON(w, 200, map[string]any{"publisher": publisherResponse(&p)})
}

// POST /api/publishers/regenerate-key — regenerate API key
func RegenerateKey(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	apiKey := strings.TrimPrefix(auth, "Bearer ")
	if apiKey == "" || apiKey == auth {
		WriteJSON(w, 401, map[string]any{"error": "Authorization required"})
		return
	}

	var publisher models.Publisher
	if err := database.DB.Where("api_key = ?", apiKey).First(&publisher).Error; err != nil {
		WriteJSON(w, 401, map[string]any{"error": "Invalid API key"})
		return
	}

	newKey := models.GenerateAPIKey()
	database.DB.Model(&publisher).Update("api_key", newKey)

	WriteJSON(w, 200, map[string]any{
		"apiKey":  newKey,
		"message": "New API key generated. Save it — it will not be shown again.",
	})
}
