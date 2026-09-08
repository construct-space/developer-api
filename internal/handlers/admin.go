package handlers

import (
	"net/http"
	"time"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
)

// GET /api/admin/spaces — list all spaces (admin only)
// Gated by X-Internal-Secret so only Oracle (the staff admin) can reach
// this through its proxy. The previous implementation accepted any
// logged-in developer session, which let any authenticated user see
// every space in the registry (P0 from code review).
func AdminListSpaces(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	var spaces []models.Space
	database.DB.Order("CASE WHEN status = 'pending_review' THEN 0 WHEN status = 'approved' THEN 1 ELSE 2 END, updated_at DESC").Find(&spaces)

	var items []map[string]any
	for _, s := range spaces {
		items = append(items, s.ToJSON())
	}
	if items == nil {
		items = []map[string]any{}
	}

	WriteJSON(w, 200, map[string]any{
		"spaces": items,
		"total":  len(items),
	})
}

// POST /api/admin/spaces/{name}/review — approve or reject a space
// Gated by X-Internal-Secret (see AdminListSpaces comment). The newer
// AdminApproveSpace / AdminRejectSpace handlers are the preferred path
// via Oracle; this route is kept for backwards compatibility with any
// existing caller, now with the same auth contract.
func AdminReviewSpace(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	spaceName := r.PathValue("name")
	if spaceName == "" {
		WriteJSON(w, 400, map[string]any{"error": "Space name required"})
		return
	}

	body, err := parseBody(r)
	if err != nil {
		WriteJSON(w, 400, map[string]any{"error": "Invalid JSON"})
		return
	}

	action, _ := body["action"].(string)
	notes, _ := body["notes"].(string)

	var space models.Space
	if err := database.DB.Where("name = ?", spaceName).First(&space).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	now := time.Now()
	updates := map[string]any{
		"reviewed_at": &now,
	}

	switch action {
	case "approve":
		updates["status"] = "approved"
		if notes != "" {
			updates["reviewer_notes"] = notes
		}
	case "reject":
		updates["status"] = "rejected"
		updates["reviewer_notes"] = notes
	default:
		WriteJSON(w, 400, map[string]any{"error": "Action must be 'approve' or 'reject'"})
		return
	}

	database.DB.Model(&space).Updates(updates)

	var updated models.Space
	database.DB.Where("name = ?", spaceName).First(&updated)

	WriteJSON(w, 200, map[string]any{
		"space":   updated.ToJSON(),
		"message": "Space " + action + "d",
	})
}
