package handlers

import (
	"net/http"

	"construct/dev-portal/internal/config"
	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
	"construct/dev-portal/internal/services"
)

// POST /internal/marketplace/backfill
//
// One-shot bootstrap (and reconcile-after-outage) endpoint. Streams every
// approved space from developer_db into marketplace-api via /internal/spaces.
// Idempotent on the marketplace side (upsert by slug), so re-running is safe.
//
// Gated by X-Internal-Secret since it makes a peer-service call per row;
// not exposed to publishers or end users.
//
// Response shape:
//
//	{ ok: true, total: N, promoted: N, failed: [...names] }
func AdminBackfillMarketplace(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}

	var spaces []models.Space
	if err := database.DB.Where("status = ?", "approved").Find(&spaces).Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "load approved spaces: " + err.Error()})
		return
	}

	cfg := config.Load()
	failed := []map[string]string{}
	promoted := 0
	for i := range spaces {
		if err := services.PromoteSpace(cfg, &spaces[i]); err != nil {
			failed = append(failed, map[string]string{
				"name":  spaces[i].Name,
				"error": err.Error(),
			})
			continue
		}
		promoted++
	}

	WriteJSON(w, 200, map[string]any{
		"ok":             true,
		"marketplaceUrl": cfg.MarketplaceURL,
		"total":          len(spaces),
		"promoted":       promoted,
		"failed":         failed,
	})
}
