package handlers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
	"construct/dev-portal/internal/services"
	"construct/dev-portal/internal/storage"
)

// GET /api/admin/spaces/{id}/publishes
// Admin-gated history list. Used by Oracle to render "who published
// version X on date Y", with per-row source-download links.
// Each row enriched with publisher's user info from the Publisher
// table where available — falls back to the raw user_id otherwise.
func AdminListSpacePublishes(w http.ResponseWriter, r *http.Request) {
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

	var publishes []models.SpacePublish
	database.DB.
		Where("space_id = ?", space.ID).
		Order("published_at DESC").
		Find(&publishes)

	WriteJSON(w, 200, map[string]any{
		"publishes": enrichPublishes(publishes),
		"total":     len(publishes),
	})
}

// GET /api/spaces/{name}/publishes
// Owner-gated history. Caller (CLI token or session) must own the
// space personally OR be in an org that owns it. Returns the same
// per-row shape as the admin endpoint so the My portal can reuse the
// rendering logic.
func ListSpacePublishes(w http.ResponseWriter, r *http.Request) {
	caller := resolveCaller(r)
	if caller == nil {
		WriteJSON(w, 401, map[string]any{"error": "Authentication required"})
		return
	}
	name := r.PathValue("name")
	if name == "" {
		WriteJSON(w, 400, map[string]any{"error": "name required"})
		return
	}

	var space models.Space
	if err := database.DB.Where("name = ?", name).First(&space).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	if !callerOwnsSpace(caller, &space) {
		WriteJSON(w, 403, map[string]any{"error": "You don't have access to this space"})
		return
	}

	var publishes []models.SpacePublish
	database.DB.
		Where("space_id = ?", space.ID).
		Order("published_at DESC").
		Find(&publishes)

	WriteJSON(w, 200, map[string]any{
		"publishes": enrichPublishes(publishes),
		"total":     len(publishes),
	})
}

// GET /api/admin/spaces/{id}/source
// Streams the source tarball for the latest version of a space.
// Admin-gated. Falls back to local filesystem if R2 isn't configured
// or the object isn't there.
func AdminDownloadLatestSource(w http.ResponseWriter, r *http.Request) {
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
	streamSource(w, r, space.Name, space.Version, space.SourcePath)
}

// GET /api/admin/publishes/{id}/source
// Streams the source tarball for a specific publish row. Lets Oracle
// fetch any historical version, not just the latest.
func AdminDownloadPublishSource(w http.ResponseWriter, r *http.Request) {
	if !requireInternalSecret(w, r) {
		return
	}
	idStr := r.PathValue("id")
	pid, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || pid == 0 {
		WriteJSON(w, 400, map[string]any{"error": "id required"})
		return
	}
	var pub models.SpacePublish
	if err := database.DB.First(&pub, "id = ?", pid).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Publish not found"})
		return
	}
	var space models.Space
	if err := database.DB.First(&space, "id = ?", pub.SpaceID).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}
	streamSource(w, r, space.Name, pub.Version, pub.SourcePath)
}

// streamSource is shared by the admin source-download endpoints. Tries
// R2 first (the canonical store for new publishes), then the legacy
// local-filesystem path for spaces published before R2 was wired up.
// Returns 404 if neither path resolves to a file — keeps the JSON
// error shape consistent with the rest of the API.
func streamSource(w http.ResponseWriter, r *http.Request, spaceName, version string, sourcePathPtr *string) {
	r2, err := storage.NewR2FromEnv()
	if err == nil {
		key := storage.SourceKey(spaceName, version)
		obj, getErr := r2.GetObject(key)
		if getErr == nil && obj.Body != nil {
			defer obj.Body.Close()
			w.Header().Set("Content-Type", "application/gzip")
			w.Header().Set("Content-Disposition",
				fmt.Sprintf("attachment; filename=%s-%s-source.tar.gz", spaceName, version))
			if obj.ContentLength != nil {
				w.Header().Set("Content-Length", fmt.Sprintf("%d", *obj.ContentLength))
			}
			_, _ = io.Copy(w, obj.Body)
			return
		}
	}

	// Filesystem fallback. Honour the recorded SourcePath if it's a
	// local path; otherwise reconstruct from the conventional layout.
	candidate := ""
	if sourcePathPtr != nil {
		s := strings.TrimSpace(*sourcePathPtr)
		if s != "" && !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
			candidate = s
		}
	}
	if candidate == "" {
		candidate = filepath.Join("data", "sources", spaceName, version, "source.tar.gz")
	}
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		WriteJSON(w, 404, map[string]any{"error": "Source archive not found"})
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%s-%s-source.tar.gz", spaceName, version))
	http.ServeFile(w, r, candidate)
}

// enrichPublishes turns SpacePublish rows into JSON-friendly maps and
// joins per-row publisher details. Lookup order:
//  1. Publisher table by user_id — covers personal-enrolled publishers
//     (their org publish too, when they happen to also have a personal
//     row). Has the publisher's preferred name + email.
//  2. accounts /internal/users/batch — covers org publishes where the
//     user has no personal Publisher row, which is the common case for
//     non-developer-flagged org members. Falls back to first/last name.
// Frontend renders publisher.name + publisher.email when present, or a
// truncated UUID otherwise — so a transient accounts outage degrades
// gracefully rather than erroring.
func enrichPublishes(rows []models.SpacePublish) []map[string]any {
	if len(rows) == 0 {
		return []map[string]any{}
	}
	userIDs := map[string]bool{}
	for _, p := range rows {
		if p.PublisherUserID != "" {
			userIDs[p.PublisherUserID] = true
		}
	}
	ids := make([]string, 0, len(userIDs))
	for id := range userIDs {
		ids = append(ids, id)
	}

	publisherByUser := map[string]models.Publisher{}
	if len(ids) > 0 {
		var pubs []models.Publisher
		database.DB.Where("user_id IN ?", ids).Find(&pubs)
		for _, p := range pubs {
			if p.UserID != nil {
				publisherByUser[*p.UserID] = p
			}
		}
	}

	// Fall back to accounts only for users we couldn't find in Publisher.
	// Skipping the trip when every user is already covered keeps the
	// common (personal-publisher) path zero-RTT.
	missing := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := publisherByUser[id]; !ok {
			missing = append(missing, id)
		}
	}
	userByID := map[string]services.UserInfo{}
	if len(missing) > 0 {
		userByID = services.BatchUsers(Cfg, missing)
	}

	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		row := map[string]any{
			"id":              p.ID,
			"spaceId":         p.SpaceID,
			"version":         p.Version,
			"publisherUserId": p.PublisherUserID,
			"publishedAt":     p.PublishedAt,
			"buildSize":       p.BuildSize,
			"hasSource":       p.HasSource(),
		}
		if p.OwnerUserID != nil {
			row["ownerUserId"] = *p.OwnerUserID
		}
		if p.OwnerOrgID != nil {
			row["ownerOrgId"] = *p.OwnerOrgID
		}
		if p.BundleURL != nil {
			row["bundleUrl"] = *p.BundleURL
		}
		if p.BuildChecksum != nil {
			row["buildChecksum"] = *p.BuildChecksum
		}
		if p.BuildDuration != nil {
			row["buildDuration"] = *p.BuildDuration
		}
		if pub, ok := publisherByUser[p.PublisherUserID]; ok {
			row["publisher"] = map[string]any{
				"name":  pub.Name,
				"email": pub.Email,
				"kind":  pub.Kind(),
			}
		} else if u, ok := userByID[p.PublisherUserID]; ok {
			row["publisher"] = map[string]any{
				"name":  u.Name,
				"email": u.Email,
				"kind":  "user",
			}
		}
		out = append(out, row)
	}
	return out
}

// callerOwnsSpace authorizes ListSpacePublishes. Personal owners get
// access via OwnerUserID equality; org members get access when the
// space's OwnerOrgID matches their active org. Backends checking
// publish history don't need finer-grained role gating — the my
// portal already restricts visibility of org spaces to org members.
func callerOwnsSpace(c *callerIdentity, s *models.Space) bool {
	if c == nil || s == nil {
		return false
	}
	if s.OwnerOrgID != nil && c.OrgID != "" && *s.OwnerOrgID == c.OrgID {
		return true
	}
	if s.OwnerUserID != nil && *s.OwnerUserID == c.UserID {
		return true
	}
	return false
}
