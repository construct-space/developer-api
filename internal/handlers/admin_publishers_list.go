package handlers

import (
	"net/http"
	"strconv"
	"time"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
)

// GET /api/admin/publishers
// Lists all publishers with pagination. Supports q (name/email search), kind, verified filters.
// Gated by X-Internal-Secret so oracle can proxy it.
func AdminListPublishers(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	q := r.URL.Query().Get("q")
	kind := r.URL.Query().Get("kind")
	verifiedParam := r.URL.Query().Get("verified")
	pageParam := r.URL.Query().Get("page")
	limitParam := r.URL.Query().Get("limit")

	page := 1
	limit := 50
	if v, err := strconv.Atoi(pageParam); err == nil && v > 0 {
		page = v
	}
	if v, err := strconv.Atoi(limitParam); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	offset := (page - 1) * limit

	db := database.DB.Model(&models.Publisher{})
	if q != "" {
		like := "%" + q + "%"
		db = db.Where("name LIKE ? OR email LIKE ?", like, like)
	}
	if kind != "" {
		switch kind {
		case "user":
			db = db.Where("user_id IS NOT NULL AND user_id != ''")
		case "org":
			db = db.Where("org_id IS NOT NULL AND org_id != ''")
		case "legacy":
			db = db.Where("(user_id IS NULL OR user_id = '') AND (org_id IS NULL OR org_id = '')")
		}
	}
	if verifiedParam != "" {
		db = db.Where("verified = ?", verifiedParam == "true")
	}

	var total int64
	db.Count(&total)

	var publishers []models.Publisher
	db.Order("created_at DESC").Offset(offset).Limit(limit).Find(&publishers)

	items := make([]map[string]any, 0, len(publishers))
	for _, p := range publishers {
		var spaceCount int64
		database.DB.Model(&models.Space{}).Where("publisher_name = ?", p.Name).Count(&spaceCount)

		item := map[string]any{
			"id":          p.ID,
			"name":        p.Name,
			"email":       p.Email,
			"kind":        p.Kind(),
			"verified":    p.Verified,
			"space_count": spaceCount,
			"created_at":  nil,
		}
		if p.CreatedAt != nil {
			item["created_at"] = p.CreatedAt.Format(time.RFC3339)
		}
		if p.Website != nil {
			item["website"] = *p.Website
		}
		if p.AvatarURL != nil {
			item["avatar_url"] = *p.AvatarURL
		}
		items = append(items, item)
	}

	WriteJSON(w, 200, map[string]any{
		"publishers": items,
		"total":      total,
		"page":       page,
		"limit":      limit,
	})
}

// GET /api/admin/publishers/{id}
// Returns a single publisher with API keys and member users.
// Gated by X-Internal-Secret.
func AdminGetPublisher(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		WriteJSON(w, 400, map[string]any{"error": "id required"})
		return
	}

	var p models.Publisher
	if err := database.DB.First(&p, id).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Publisher not found"})
		return
	}

	var spaces []models.Space
	database.DB.Where("publisher_name = ?", p.Name).Order("updated_at DESC").Find(&spaces)

	spaceItems := make([]map[string]any, 0, len(spaces))
	for _, s := range spaces {
		spaceItems = append(spaceItems, s.ToJSON())
	}

	result := map[string]any{
		"id":         p.ID,
		"name":       p.Name,
		"email":      p.Email,
		"kind":       p.Kind(),
		"verified":   p.Verified,
		"api_key":    p.APIKey,
		"spaces":     spaceItems,
		"created_at": nil,
	}
	if p.CreatedAt != nil {
		result["created_at"] = p.CreatedAt.Format(time.RFC3339)
	}
	if p.Website != nil {
		result["website"] = *p.Website
	}
	if p.AvatarURL != nil {
		result["avatar_url"] = *p.AvatarURL
	}
	if p.UserID != nil {
		result["user_id"] = *p.UserID
	}
	if p.OrgID != nil {
		result["org_id"] = *p.OrgID
	}

	WriteJSON(w, 200, result)
}
