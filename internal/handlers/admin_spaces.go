package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"construct/dev-portal/internal/config"
	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
	"construct/dev-portal/internal/services"
)

// GET /api/admin/spaces/{id}
func AdminGetSpace(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		WriteJSON(w, 400, map[string]any{"error": "id required"})
		return
	}

	var space models.Space
	if err := findSpaceByIDOrName(id, &space); err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	item := space.ToJSON()

	item["build_size"] = space.BuildSize
	if space.BuildDuration != nil {
		item["build_duration"] = *space.BuildDuration
	}

	if space.PublisherName != nil && *space.PublisherName != "" {
		var pub models.Publisher
		if err := database.DB.Where("name = ?", *space.PublisherName).First(&pub).Error; err == nil {
			item["publisher_info"] = map[string]any{
				"id":       pub.ID,
				"name":     pub.Name,
				"email":    pub.Email,
				"kind":     pub.Kind(),
				"verified": pub.Verified,
			}
		}
	}

	WriteJSON(w, 200, item)
}

// PUT /api/admin/spaces/{id}
// Accepts: display_name, description, icon, scope, author, recommended
func AdminUpdateSpace(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		WriteJSON(w, 400, map[string]any{"error": "id required"})
		return
	}

	body, err := parseBody(r)
	if err != nil {
		WriteJSON(w, 400, map[string]any{"error": "Invalid JSON"})
		return
	}

	var space models.Space
	if err := findSpaceByIDOrName(id, &space); err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	updates := map[string]any{}
	if v, ok := body["display_name"].(string); ok && v != "" {
		updates["display_name"] = v
	}
	if v, ok := body["description"].(string); ok {
		updates["description"] = v
	}
	if v, ok := body["icon"].(string); ok && v != "" {
		updates["icon"] = v
	}
	if rawScopes, ok := body["scopes"].([]any); ok {
		filtered := []string{}
		for _, v := range rawScopes {
			if s, ok := v.(string); ok && (s == "app" || s == "org") {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) > 0 {
			b, _ := json.Marshal(filtered)
			updates["scopes_json"] = string(b)
		}
	}
	if v, ok := body["projectAware"].(bool); ok {
		updates["project_aware"] = v
	}
	if v, ok := body["author"].(string); ok && v != "" {
		updates["author"] = v
	}
	if v, ok := body["recommended"].(bool); ok {
		updates["recommended"] = v
	}
	if v, ok := body["status"].(string); ok && v != "" {
		switch v {
		case "draft", "pending_review", "approved", "rejected", "changes_requested":
			updates["status"] = v
			if v == "approved" || v == "rejected" || v == "changes_requested" {
				now := time.Now()
				updates["reviewed_at"] = &now
			}
		default:
			WriteJSON(w, 400, map[string]any{"error": "invalid status"})
			return
		}
	}

	if len(updates) == 0 {
		WriteJSON(w, 400, map[string]any{"error": "No updatable fields provided"})
		return
	}

	if err := database.DB.Model(&space).Updates(updates).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "Failed to update space"})
		return
	}

	WriteJSON(w, 200, map[string]any{"message": "Space updated", "id": space.Name})
}

// POST /api/admin/spaces/{id}/unpublish
func AdminUnpublishSpace(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		WriteJSON(w, 400, map[string]any{"error": "id required"})
		return
	}

	var space models.Space
	if err := findSpaceByIDOrName(id, &space); err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	if err := database.DB.Model(&space).Updates(map[string]any{
		"status":      "draft",
		"reviewed_at": (*time.Time)(nil),
	}).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "Failed to unpublish space"})
		return
	}

	// Soft-remove from marketplace catalog. Best-effort: backfill endpoint
	// can fix up later.
	services.DemoteSpaceBestEffort(config.Load(), space.Name)

	WriteJSON(w, 200, map[string]any{"message": "Space unpublished", "id": space.Name})
}

// POST /api/admin/spaces/{id}/toggle-recommended
func AdminToggleRecommended(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		WriteJSON(w, 400, map[string]any{"error": "id required"})
		return
	}

	var space models.Space
	if err := findSpaceByIDOrName(id, &space); err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	if err := database.DB.Model(&space).Update("recommended", !space.Recommended).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "Failed to toggle recommended"})
		return
	}

	WriteJSON(w, 200, map[string]any{"message": "Recommended toggled", "recommended": !space.Recommended, "id": space.Name})
}

// POST /api/admin/spaces/{id}/transfer
//
// Body: { "to_user_id": "..." } or { "to_org_id": "..." } — exactly one.
// Admin override: directly rewrites ownership without going through the
// propose/accept transfer flow. Skips the 7-day re-transfer lock and the
// in-review guardrail because Oracle staff are expected to use this for
// remediation (e.g. moving a space from a personal account that disappeared
// to the rightful org). Use the regular CreateTransfer endpoint for normal
// owner-initiated transfers.
func AdminTransferSpace(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		WriteJSON(w, 400, map[string]any{"error": "id required"})
		return
	}

	body, err := parseBody(r)
	if err != nil {
		WriteJSON(w, 400, map[string]any{"error": "Invalid JSON"})
		return
	}
	toUserID, _ := body["to_user_id"].(string)
	toOrgID, _ := body["to_org_id"].(string)
	if (toUserID == "") == (toOrgID == "") {
		WriteJSON(w, 400, map[string]any{"error": "exactly one of to_user_id or to_org_id is required"})
		return
	}

	var space models.Space
	if err := findSpaceByIDOrName(id, &space); err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	updates := map[string]any{
		"owner_user_id": (*string)(nil),
		"owner_org_id":  (*string)(nil),
	}
	if toUserID != "" {
		updates["owner_user_id"] = toUserID
	} else {
		updates["owner_org_id"] = toOrgID
	}
	if err := database.DB.Model(&space).Updates(updates).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "Failed to transfer space"})
		return
	}

	WriteJSON(w, 200, map[string]any{
		"message":     "Space transferred",
		"id":          space.Name,
		"ownerUserId": toUserID,
		"ownerOrgId":  toOrgID,
	})
}

// DELETE /api/admin/spaces/{id}
//
// Hard-deletes a space row. Intentionally heavier than AdminUnpublishSpace
// (which just flips status to draft) — this is the Oracle staff "remove
// from the registry entirely" action. Bundle blobs and graph schemas are
// not purged here; they belong to other services and clean up via their
// own retention jobs.
func AdminDeleteSpace(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		WriteJSON(w, 400, map[string]any{"error": "id required"})
		return
	}

	var space models.Space
	if err := findSpaceByIDOrName(id, &space); err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	if err := database.DB.Delete(&space).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "Failed to delete space"})
		return
	}

	WriteJSON(w, 200, map[string]any{"message": "Space deleted", "id": space.Name})
}

// findSpaceByIDOrName looks up a Space by numeric primary key if id parses as
// an integer, otherwise falls back to the unique `name` column. Oracle's admin
// console lists spaces keyed by name for display, so the action endpoints
// receive the name in the URL path.
func findSpaceByIDOrName(id string, space *models.Space) error {
	if n, err := strconv.ParseUint(id, 10, 64); err == nil {
		return database.DB.First(space, uint(n)).Error
	}
	return database.DB.Where("name = ?", id).First(space).Error
}

// GET /api/admin/spaces/pending-review
// Lists spaces with status='pending_review', joined with publisher and owning-user info.
func AdminListPendingSpaces(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	var spaces []models.Space
	if err := database.DB.Where("status = ?", "pending_review").Order("updated_at ASC").Find(&spaces).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "Failed to list pending spaces"})
		return
	}

	items := make([]map[string]any, 0, len(spaces))
	for _, s := range spaces {
		item := s.ToJSON()

		// Enrich with publisher record if publisher_name is set
		if s.PublisherName != nil && *s.PublisherName != "" {
			var pub models.Publisher
			if err := database.DB.Where("name = ?", *s.PublisherName).First(&pub).Error; err == nil {
				item["publisher_info"] = map[string]any{
					"id":       pub.ID,
					"name":     pub.Name,
					"email":    pub.Email,
					"kind":     pub.Kind(),
					"verified": pub.Verified,
				}
			}
		}

		items = append(items, item)
	}

	WriteJSON(w, 200, map[string]any{
		"spaces": items,
		"total":  len(items),
	})
}

// POST /api/admin/spaces/{id}/approve
func AdminApproveSpace(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		WriteJSON(w, 400, map[string]any{"error": "id required"})
		return
	}

	var space models.Space
	if err := findSpaceByIDOrName(id, &space); err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	now := time.Now()
	if err := database.DB.Model(&space).Updates(map[string]any{
		"status":      "approved",
		"reviewed_at": &now,
	}).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "Failed to approve space"})
		return
	}

	// Push to marketplace catalog. Best-effort: a marketplace outage must
	// not roll back the approve. The /internal/marketplace/backfill admin
	// endpoint can re-sync any rows that fail here.
	services.PromoteSpaceBestEffort(config.Load(), &space)

	WriteJSON(w, 200, map[string]any{"message": "Space approved", "id": space.ID})
}

// POST /api/admin/spaces/{id}/reject
// Body: { "reason": "..." }
func AdminRejectSpace(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		WriteJSON(w, 400, map[string]any{"error": "id required"})
		return
	}

	body, err := parseBody(r)
	if err != nil {
		WriteJSON(w, 400, map[string]any{"error": "Invalid JSON"})
		return
	}

	reason, _ := body["reason"].(string)

	var space models.Space
	if err := findSpaceByIDOrName(id, &space); err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	now := time.Now()
	updates := map[string]any{
		"status":      "rejected",
		"reviewed_at": &now,
	}
	if reason != "" {
		updates["reviewer_notes"] = reason
	}

	if err := database.DB.Model(&space).Updates(updates).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "Failed to reject space"})
		return
	}

	WriteJSON(w, 200, map[string]any{"message": "Space rejected", "id": space.ID})
}

// POST /api/admin/spaces/{id}/request-changes
// Body: { "note": "..." }
func AdminRequestChangesSpace(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		WriteJSON(w, 400, map[string]any{"error": "id required"})
		return
	}

	body, err := parseBody(r)
	if err != nil {
		WriteJSON(w, 400, map[string]any{"error": "Invalid JSON"})
		return
	}

	note, _ := body["note"].(string)

	var space models.Space
	if err := findSpaceByIDOrName(id, &space); err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	now := time.Now()
	updates := map[string]any{
		"status":      "changes_requested",
		"reviewed_at": &now,
	}
	if note != "" {
		updates["reviewer_notes"] = note
	}

	if err := database.DB.Model(&space).Updates(updates).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "Failed to request changes"})
		return
	}

	WriteJSON(w, 200, map[string]any{"message": "Changes requested", "id": space.ID})
}
