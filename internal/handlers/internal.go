package handlers

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
)

// requireInternalSecret gates service-to-service endpoints with a shared secret.
// Returns true if the caller is authorized. Writes 401 otherwise.
func requireInternalSecret(w http.ResponseWriter, r *http.Request) bool {
	if Cfg.InternalSecret == "" {
		// Not configured — refuse all internal calls rather than allow unauth access.
		WriteJSON(w, 503, map[string]any{"error": "internal endpoints disabled"})
		return false
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Secret")), []byte(Cfg.InternalSecret)) != 1 {
		WriteJSON(w, 401, map[string]any{"error": "unauthorized"})
		return false
	}
	return true
}

// GET /internal/publisher?user_id=<uuid>
// Returns the personal Publisher for a given user UUID, or 404 if none.
// Called by accounts during /me enrichment to resolve personal developer capability.
func InternalGetPublisherByUser(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		WriteJSON(w, 400, map[string]any{"error": "user_id required"})
		return
	}

	var p models.Publisher
	err := database.DB.Where("user_id = ?", userID).First(&p).Error
	if err != nil {
		WriteJSON(w, 404, map[string]any{"error": "not found"})
		return
	}

	WriteJSON(w, 200, map[string]any{
		"publisher_id": p.ID,
		"name":         p.Name,
		"email":        p.Email,
		"user_id":      p.UserID,
		"verified":     p.Verified,
	})
}

// POST /internal/spaces/archive-for-org
// Body: { "org_id": "..." }
// Called by source when an org is being deleted. Published spaces are NOT
// deleted (NPM-style: artifacts others depend on must persist). Instead:
//   - Space.Status → "archived"
//   - Space.OwnerOrgID cleared (downstream admin queue assigns a new owner)
//   - Publisher row for the org is deleted (no more publishing under it)
//   - Developer role cleanup happens via source's /internal/developer-role/unseed
func InternalArchiveSpacesForOrg(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	var body struct {
		OrgID string `json:"org_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.OrgID == "" {
		WriteJSON(w, 400, map[string]any{"error": "org_id required"})
		return
	}

	tx := database.DB.Begin()
	var archived int64
	res := tx.Model(&models.Space{}).
		Where("owner_org_id = ?", body.OrgID).
		Updates(map[string]any{"status": "archived", "owner_org_id": nil})
	if res.Error != nil {
		tx.Rollback()
		WriteJSON(w, 500, map[string]any{"error": "archive failed"})
		return
	}
	archived = res.RowsAffected

	// Cancel any pending transfers where the org was either source or target.
	tx.Model(&models.SpaceTransfer{}).
		Where("status = ? AND (from_org_id = ? OR to_org_id = ?)", "pending", body.OrgID, body.OrgID).
		Updates(map[string]any{"status": "cancelled"})

	// Retire the org's Publisher row — org no longer exists to publish.
	var deletedPublishers int64
	del := tx.Where("org_id = ?", body.OrgID).Delete(&models.Publisher{})
	if del.Error == nil {
		deletedPublishers = del.RowsAffected
	}

	if err := tx.Commit().Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "commit failed"})
		return
	}

	WriteJSON(w, 200, map[string]any{
		"archived_spaces":    archived,
		"deleted_publishers": deletedPublishers,
	})
}

// POST /internal/publishers/backfill
// Body: { "users": [ { "user_id": "...", "email": "..." }, ... ] }
// Backfills user_id on legacy Publisher rows whose email matches an entry in
// the payload and whose user_id is currently null/empty. One-shot migration
// helper called from an admin script after the identity-scopes rollout.
// Returns counts { matched, linked, already_linked, skipped }.
func InternalBackfillPublishers(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	var body struct {
		Users []struct {
			UserID string `json:"user_id"`
			Email  string `json:"email"`
		} `json:"users"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]any{"error": "invalid body"})
		return
	}

	matched, linked, alreadyLinked, skipped := 0, 0, 0, 0
	for _, u := range body.Users {
		if u.Email == "" || u.UserID == "" {
			skipped++
			continue
		}
		var p models.Publisher
		if err := database.DB.Where("email = ?", u.Email).First(&p).Error; err != nil {
			skipped++
			continue
		}
		matched++
		if p.UserID != nil && *p.UserID != "" {
			alreadyLinked++
			continue
		}
		if err := database.DB.Model(&p).Update("user_id", u.UserID).Error; err != nil {
			skipped++
			continue
		}
		linked++
	}

	WriteJSON(w, 200, map[string]any{
		"matched":        matched,
		"linked":         linked,
		"already_linked": alreadyLinked,
		"skipped":        skipped,
	})
}

// GET /internal/publisher/org?org_id=<uuid>
// Returns the org Publisher, or 404. Used to check whether an org is enrolled
// as a publisher when the app asks "is my org a developer-org".
func InternalGetPublisherByOrg(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	orgID := r.URL.Query().Get("org_id")
	if orgID == "" {
		WriteJSON(w, 400, map[string]any{"error": "org_id required"})
		return
	}

	var p models.Publisher
	err := database.DB.Where("org_id = ?", orgID).First(&p).Error
	if err != nil {
		WriteJSON(w, 404, map[string]any{"error": "not found"})
		return
	}

	WriteJSON(w, 200, map[string]any{
		"publisher_id": p.ID,
		"name":         p.Name,
		"org_id":       p.OrgID,
		"verified":     p.Verified,
	})
}
