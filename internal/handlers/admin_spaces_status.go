package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
)

// POST /api/admin/spaces/status
// Body: { "names": ["space-a", "space-b", ...] }
// Response: { "statuses": { "space-a": { ...status... }, ... } }
//
// Batch lookup of marketplace-lifecycle status for a list of space names.
// Used by Oracle to enrich graph-sourced space lists without N+1 calls —
// graph owns the canonical "spaces exist" view (every space with runtime
// presence), developer owns the "marketplace status" subset (only spaces
// that went through `construct space publish`). Names not found in the
// developer database are simply omitted from the response so callers can
// render those rows as "not submitted".
//
// Capped at 500 names per request — well above what one Oracle list page
// shows, low enough that a malicious / bug-loop caller can't fan a single
// HTTP request into a runaway query.
func AdminBatchSpaceStatus(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	var body struct {
		Names []string `json:"names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]any{"error": "invalid body"})
		return
	}
	if len(body.Names) == 0 {
		WriteJSON(w, 200, map[string]any{"statuses": map[string]any{}})
		return
	}
	if len(body.Names) > 500 {
		body.Names = body.Names[:500]
	}

	var spaces []models.Space
	if err := database.DB.
		Where("name IN ?", body.Names).
		Find(&spaces).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "lookup failed"})
		return
	}

	// Pre-fetch all referenced publishers in one query so per-row enrichment
	// stays O(1). Keyed by Publisher.Name since Space.PublisherName is the
	// link column.
	publisherNames := map[string]bool{}
	for _, s := range spaces {
		if s.PublisherName != nil && *s.PublisherName != "" {
			publisherNames[*s.PublisherName] = true
		}
	}
	pubByName := map[string]models.Publisher{}
	if len(publisherNames) > 0 {
		names := make([]string, 0, len(publisherNames))
		for n := range publisherNames {
			names = append(names, n)
		}
		var pubs []models.Publisher
		database.DB.Where("name IN ?", names).Find(&pubs)
		for _, p := range pubs {
			pubByName[p.Name] = p
		}
	}

	out := make(map[string]any, len(spaces))
	for _, s := range spaces {
		row := map[string]any{
			"id":          s.ID,
			"status":      s.Status,
			"version":     s.Version,
			"recommended": s.Recommended,
			"buildSize":   s.BuildSize,
		}
		if s.ReviewedAt != nil {
			row["reviewedAt"] = s.ReviewedAt.Format(time.RFC3339)
		}
		if s.UpdatedAt != nil {
			row["updatedAt"] = s.UpdatedAt.Format(time.RFC3339)
		}
		if s.SubmittedBy != nil {
			row["submittedBy"] = *s.SubmittedBy
		}
		if s.OwnerOrgID != nil {
			row["ownerOrgId"] = *s.OwnerOrgID
		} else if s.OwnerUserID != nil {
			row["ownerUserId"] = *s.OwnerUserID
		}
		if s.PublisherName != nil {
			if pub, ok := pubByName[*s.PublisherName]; ok {
				row["publisher"] = map[string]any{
					"id":       pub.ID,
					"name":     pub.Name,
					"email":    pub.Email,
					"kind":     pub.Kind(),
					"verified": pub.Verified,
				}
			} else {
				row["publisher"] = map[string]any{"name": *s.PublisherName}
			}
		}
		out[s.Name] = row
	}

	WriteJSON(w, 200, map[string]any{"statuses": out})
}
