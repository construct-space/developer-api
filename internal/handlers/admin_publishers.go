package handlers

import (
	"net/http"
	"time"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
)

// GET /api/admin/publishers/by-user/{user_id}
// Returns the publisher record for a given user, including API key (unmasked — masking is frontend's job)
// and owned space count. Gated by session auth (existing admin middleware pattern).
func AdminGetPublisherByUser(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	userID := r.PathValue("user_id")
	if userID == "" {
		WriteJSON(w, 400, map[string]any{"error": "user_id required"})
		return
	}

	var p models.Publisher
	if err := database.DB.Where("user_id = ?", userID).First(&p).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Publisher not found"})
		return
	}

	var spaceCount int64
	database.DB.Model(&models.Space{}).Where("publisher_name = ?", p.Name).Count(&spaceCount)

	result := map[string]any{
		"id":          p.ID,
		"name":        p.Name,
		"email":       p.Email,
		"kind":        p.Kind(),
		"verified":    p.Verified,
		"api_key":     p.APIKey,
		"space_count": spaceCount,
		"created_at":  nil,
	}
	if p.Website != nil {
		result["website"] = *p.Website
	}
	if p.AvatarURL != nil {
		result["avatar_url"] = *p.AvatarURL
	}
	if p.CreatedAt != nil {
		result["created_at"] = p.CreatedAt.Format(time.RFC3339)
	}

	WriteJSON(w, 200, result)
}

// PUT /api/admin/publishers/{id}/verify
func AdminVerifyPublisher(w http.ResponseWriter, r *http.Request) {
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

	if err := database.DB.Model(&p).Update("verified", true).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "Failed to verify publisher"})
		return
	}

	WriteJSON(w, 200, map[string]any{"id": p.ID, "verified": true})
}

// PUT /api/admin/publishers/{id}/unverify
func AdminUnverifyPublisher(w http.ResponseWriter, r *http.Request) {
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

	if err := database.DB.Model(&p).Update("verified", false).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "Failed to unverify publisher"})
		return
	}

	WriteJSON(w, 200, map[string]any{"id": p.ID, "verified": false})
}
