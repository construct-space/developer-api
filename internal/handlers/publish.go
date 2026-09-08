package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"construct/dev-portal/internal/builder"
	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
	"construct/dev-portal/internal/storage"
)

// parseBoolForm reads a multipart-form bool. Accepts "1", "true", "yes"
// (case-insensitive). Empty / anything else → false.
func parseBoolForm(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

const maxUploadSize = 60 * 1024 * 1024 // 60MB

// isValidVersion accepts a conservative semver-ish version string and, most
// importantly, rejects anything usable for path traversal. Allowed: ASCII
// letters, digits, '.', '+', '-'; must be non-empty, <=64 chars, and contain
// no ".." sequence (so it can't escape a directory when used as a path
// segment or R2 key component).
func isValidVersion(v string) bool {
	if v == "" || len(v) > 64 || strings.Contains(v, "..") {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '+' || c == '-':
		default:
			return false
		}
	}
	return true
}

// POST /api/publish — receive source tarball from CLI, build, and publish.
// Multipart form with fields: "manifest" (JSON) and "source" (tar.gz file).
func PublishSpace(w http.ResponseWriter, r *http.Request) {
	// Authenticate via CLI token
	token := getCLIToken(r)
	if token == nil {
		WriteJSON(w, 401, map[string]any{"error": "Invalid or expired token"})
		return
	}

	// SECURITY: require developer enrollment (a Publisher row for this
	// identity) before doing any work. An accounts cat_* token resolves for
	// ANY logged-in user, and downstream owner checks don't gate enrollment —
	// without this, any user could trigger a server-side build (which runs
	// `bun install --trust`, i.e. arbitrary postinstall) and flood the review
	// queue. Mirrors the gate in the cli-login flow.
	var publisher models.Publisher
	pq := database.DB
	if token.OrgID != "" {
		pq = pq.Where("org_id = ?", token.OrgID)
	} else {
		pq = pq.Where("user_id = ? OR email = ?", token.UserID, token.Email)
	}
	if err := pq.First(&publisher).Error; err != nil {
		WriteJSON(w, 403, map[string]any{"error": "Developer enrollment required to publish. Visit lisaos.dev/settings to enroll."})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		WriteJSON(w, 400, map[string]any{"error": "Request too large or invalid multipart form"})
		return
	}

	// Parse manifest JSON
	manifestStr := r.FormValue("manifest")
	if manifestStr == "" {
		WriteJSON(w, 400, map[string]any{"error": "manifest field is required"})
		return
	}

	var manifest map[string]any
	if err := json.Unmarshal([]byte(manifestStr), &manifest); err != nil {
		WriteJSON(w, 400, map[string]any{"error": "Invalid manifest JSON"})
		return
	}

	spaceName, _ := manifest["id"].(string)
	if spaceName == "" {
		WriteJSON(w, 400, map[string]any{"error": "Manifest must contain an 'id' field"})
		return
	}
	// SECURITY: spaceName and version are spliced into filesystem paths
	// (filepath.Join under data/bundles, data/sources) and R2 object keys.
	// Without validation, an id like "../../tmp/x" or a version of ".." is a
	// path-traversal / arbitrary-write primitive. isValidSlug (the same gate
	// CreateSpace uses) allows only [a-z0-9-]; reject anything else here too.
	if !isValidSlug(spaceName) {
		WriteJSON(w, 400, map[string]any{"error": "Manifest 'id' is not a valid slug (lowercase letters, digits, hyphens)"})
		return
	}

	version, _ := manifest["version"].(string)
	if version == "" {
		WriteJSON(w, 400, map[string]any{"error": "Manifest must contain a 'version' field"})
		return
	}
	if !isValidVersion(version) {
		WriteJSON(w, 400, map[string]any{"error": "Manifest 'version' contains invalid characters"})
		return
	}

	// Save uploaded source tarball to temp file
	file, _, err := r.FormFile("source")
	if err != nil {
		WriteJSON(w, 400, map[string]any{"error": "source file is required"})
		return
	}
	defer file.Close()

	tmpFile, err := os.CreateTemp("", "upload-source-*.tar.gz")
	if err != nil {
		WriteJSON(w, 500, map[string]any{"error": "Failed to save uploaded file"})
		return
	}
	sourcePath := tmpFile.Name()
	defer os.Remove(sourcePath)

	if _, err := io.Copy(tmpFile, file); err != nil {
		tmpFile.Close()
		WriteJSON(w, 500, map[string]any{"error": "Failed to save uploaded file"})
		return
	}
	tmpFile.Close()

	// Build from source
	result, err := builder.BuildFromSource(sourcePath)
	if err != nil {
		log.Printf("[publish] build failed for %s v%s: %v", spaceName, version, err)
		WriteJSON(w, 200, map[string]any{
			"status": "build_failed",
			"error":  err.Error(),
			"log":    fmt.Sprintf("%v", err),
		})
		return
	}

	// Upload bundle and source to R2 (CDN) if configured, else store locally.
	// Public R2 URLs belong in bundle_url; bundle_path is reserved for local
	// filesystem fallback paths.
	var bundlePath, bundleURL, sourceDest string

	r2, r2err := storage.NewR2FromEnv()
	if r2err == nil {
		// Upload to Cloudflare R2
		bundleKey := storage.BundleKey(spaceName, version)
		if err := r2.Upload(bundleKey, result.BundlePath); err != nil {
			log.Printf("[publish] R2 bundle upload failed: %v", err)
		} else {
			bundleURL = r2.URL(bundleKey)
			log.Printf("[publish] Bundle uploaded to R2: %s", bundleURL)
		}

		sourceKey := storage.SourceKey(spaceName, version)
		if err := r2.Upload(sourceKey, sourcePath); err != nil {
			log.Printf("[publish] R2 source upload failed: %v", err)
		}

		sourceDest = r2.URL(storage.SourceKey(spaceName, version))
	}

	if bundleURL != "" {
		os.Remove(result.BundlePath)
	} else {
		// Fallback: local storage
		storageDir := filepath.Join("data", "bundles", spaceName, version)
		if err := os.MkdirAll(storageDir, 0755); err != nil {
			os.Remove(result.BundlePath)
			WriteJSON(w, 500, map[string]any{"error": "Failed to create storage directory"})
			return
		}
		bundlePath = filepath.Join(storageDir, "bundle.tar.gz")
		if err := os.Rename(result.BundlePath, bundlePath); err != nil {
			if err := copyFile(result.BundlePath, bundlePath); err != nil {
				os.Remove(result.BundlePath)
				WriteJSON(w, 500, map[string]any{"error": "Failed to store bundle"})
				return
			}
			os.Remove(result.BundlePath)
		}

		sourceDir := filepath.Join("data", "sources", spaceName, version)
		os.MkdirAll(sourceDir, 0755)
		sourceDest = filepath.Join(sourceDir, "source.tar.gz")
		_ = copyFile(sourcePath, sourceDest)
	}

	// Upsert space in database
	displayName, _ := manifest["name"].(string)
	if displayName == "" {
		displayName = spaceName
	}
	description, _ := manifest["description"].(string)
	icon, _ := manifest["icon"].(string)
	if icon == "" {
		icon = "i-lucide-box"
	}
	// scopes: ("app"|"org")[] is required by the new manifest contract.
	// Drop unknown values + fall back to ["app"] when callers send nothing
	// usable so we never persist an empty array.
	scopes := []string{}
	if rawScopes, ok := manifest["scopes"].([]any); ok {
		for _, v := range rawScopes {
			if s, ok := v.(string); ok && (s == "app" || s == "org") {
				scopes = append(scopes, s)
			}
		}
	}
	if len(scopes) == 0 {
		scopes = []string{"app"}
	}
	scopesJSON, _ := json.Marshal(scopes)
	projectAware, _ := manifest["projectAware"].(bool)
	var author string
	switch a := manifest["author"].(type) {
	case string:
		author = a
	case map[string]any:
		author, _ = a["name"].(string)
	}
	if author == "" {
		author = token.Name
	}
	hostAPIVersion, _ := manifest["hostApiVersion"].(string)
	if hostAPIVersion == "" {
		hostAPIVersion = "^0.2.0"
	}

	navJSON := "{}"
	if nav, ok := manifest["navigation"]; ok {
		b, _ := json.Marshal(nav)
		navJSON = string(b)
	}
	pagesJSON := "[]"
	if pages, ok := manifest["pages"]; ok {
		b, _ := json.Marshal(pages)
		pagesJSON = string(b)
	}
	var themeJSON *string
	if theme, ok := manifest["theme"]; ok {
		b, _ := json.Marshal(theme)
		s := string(b)
		themeJSON = &s
	}
	var toolbarJSON *string
	if toolbar, ok := manifest["toolbar"]; ok {
		b, _ := json.Marshal(toolbar)
		s := string(b)
		toolbarJSON = &s
	}

	// Check bundled agent/skill manifest fields. The .space bundle keeps
	// agentskills.io files as normal files (root SKILL.md plus optional
	// agent/config.md), so publish metadata comes from the manifest paths.
	hasAgent := false
	skillCount := 0
	if agentPath, ok := manifest["agent"].(string); ok && strings.TrimSpace(agentPath) != "" {
		hasAgent = true
	}
	if skills, ok := manifest["skills"].([]any); ok {
		for _, skill := range skills {
			if path, ok := skill.(string); ok && strings.TrimSpace(path) != "" {
				skillCount++
			}
		}
	}

	// Org-publisher path: when the caller authenticated with an org
	// publisher's API key, the resulting Space belongs to the org. Personal
	// path leaves OrgID empty and ownership stays with the user.
	publishingAsOrg := token.OrgID != ""

	// Visibility — `private=true` marks the space as org-private (only
	// members of the owning org see it in the catalog); `public=true`
	// explicitly flips a previously-private space back to the public
	// catalog. Sending neither preserves the existing visibility on
	// updates (and defaults to "public" on first publish) so a forgotten
	// flag doesn't accidentally leak an internal-only space.
	requestPrivate := parseBoolForm(r.FormValue("private"))
	requestPublic := parseBoolForm(r.FormValue("public"))
	if requestPrivate && requestPublic {
		WriteJSON(w, 400, map[string]any{
			"error": "Cannot combine --private and --public — pick one.",
		})
		return
	}
	if requestPrivate && !publishingAsOrg {
		WriteJSON(w, 400, map[string]any{
			"error": "--private requires an org publisher API key. Personal publishes are always public.",
		})
		return
	}

	var space models.Space
	err = database.DB.Where("name = ?", spaceName).First(&space).Error
	if err != nil {
		// Claim quota: max 5 spaces per personal publisher. Org publishers
		// are uncapped — orgs are expected to ship N internal spaces and
		// the cap exists to discourage personal namespace squatting, which
		// doesn't apply at the org tier.
		if !publishingAsOrg {
			var claimCount int64
			database.DB.Model(&models.Space{}).Where("owner_user_id = ?", token.UserID).Count(&claimCount)
			if claimCount >= 5 {
				WriteJSON(w, 403, map[string]any{
					"error": "Claim quota exceeded — you can own at most 5 spaces. Transfer ownership of an existing space to free a slot.",
				})
				return
			}
		}

		var bundleURLPtr, bundlePathPtr *string
		if bundleURL != "" {
			bundleURLPtr = &bundleURL
		} else {
			bundlePathPtr = &bundlePath
		}

		// First publish — neither flag means default to public. Private
		// requires an explicit opt-in.
		newVisibility := "public"
		if requestPrivate {
			newVisibility = "org"
		}

		// Create new space — caller becomes the owner. SubmittedBy and
		// PublisherUserID always carry the human user (audit trail);
		// OwnerUserID/OwnerOrgID are mutually exclusive and reflect the
		// publishing identity.
		space = models.Space{
			Name:            spaceName,
			DisplayName:     displayName,
			Description:     description,
			Icon:            icon,
			Version:         version,
			ScopesJSON:      string(scopesJSON),
			ProjectAware:    projectAware,
			Visibility:      newVisibility,
			Author:          author,
			HostAPIVersion:  hostAPIVersion,
			Status:          "pending_review",
			SubmittedBy:     &token.UserID,
			PublisherUserID: &token.UserID,
			HasAgent:        hasAgent,
			SkillCount:      skillCount,
			NavigationJSON:  navJSON,
			PagesJSON:       pagesJSON,
			ThemeJSON:       themeJSON,
			ToolbarJSON:     toolbarJSON,
			SourcePath:      &sourceDest,
			BundleURL:       bundleURLPtr,
			BundlePath:      bundlePathPtr,
			BuildLog:        &result.Log,
			BuildChecksum:   &result.Checksum,
			BuildSize:       result.Size,
			BuildDuration:   &result.Duration,
		}
		if publishingAsOrg {
			orgID := token.OrgID
			space.OwnerOrgID = &orgID
		} else {
			userID := token.UserID
			space.OwnerUserID = &userID
		}
		database.DB.Create(&space)
	} else {
		// Update existing space — only the owner can publish updates.
		// Owner is either an org or a user; check the matching column.
		switch {
		case publishingAsOrg:
			if space.OwnerOrgID == nil || *space.OwnerOrgID != token.OrgID {
				WriteJSON(w, 403, map[string]any{
					"error": "You are not the owner of this space",
				})
				return
			}
		default:
			if space.OwnerOrgID != nil {
				resp := map[string]any{
					"error":        "This space is owned by an organization — publish with the org's API key",
					"owner_kind":   "org",
					"owner_org_id": *space.OwnerOrgID,
				}
				var orgPub models.Publisher
				if err := database.DB.Where("org_id = ?", *space.OwnerOrgID).First(&orgPub).Error; err == nil {
					resp["owner_name"] = orgPub.Name
				}
				WriteJSON(w, 403, resp)
				return
			}
			if space.OwnerUserID != nil && *space.OwnerUserID != token.UserID {
				resp := map[string]any{
					"error":         "You are not the owner of this space",
					"owner_kind":    "user",
					"owner_user_id": *space.OwnerUserID,
				}
				var userPub models.Publisher
				if err := database.DB.Where("user_id = ?", *space.OwnerUserID).First(&userPub).Error; err == nil {
					resp["owner_name"] = userPub.Name
				}
				WriteJSON(w, 403, resp)
				return
			}
			// Legacy fallback: if OwnerUserID is not set yet, check PublisherUserID
			if space.OwnerUserID == nil {
				if space.PublisherUserID != nil && *space.PublisherUserID != token.UserID {
					if space.SubmittedBy != nil && *space.SubmittedBy != token.UserID {
						WriteJSON(w, 403, map[string]any{"error": "You are not the owner of this space"})
						return
					}
				}
				// Backfill OwnerUserID for legacy spaces
				space.OwnerUserID = &token.UserID
				database.DB.Model(&space).Update("owner_user_id", token.UserID)
			}
		}

		// Update — visibility is sticky: only flip when an explicit flag
		// was sent. Workflow: publish --private (test internally), publish
		// (re-publish, stays private), publish --public (release to all).
		updateVisibility := space.Visibility
		if updateVisibility == "" {
			updateVisibility = "public"
		}
		switch {
		case requestPrivate:
			updateVisibility = "org"
		case requestPublic:
			updateVisibility = "public"
		}

		updates := map[string]any{
			"display_name":      displayName,
			"description":       description,
			"icon":              icon,
			"version":           version,
			"scopes_json":       string(scopesJSON),
			"project_aware":     projectAware,
			"visibility":        updateVisibility,
			"author":            author,
			"host_api_version":  hostAPIVersion,
			"has_agent":         hasAgent,
			"skill_count":       skillCount,
			"navigation_json":   navJSON,
			"pages_json":        pagesJSON,
			"source_path":       sourceDest,
			"build_log":         result.Log,
			"build_checksum":    result.Checksum,
			"build_size":        result.Size,
			"build_duration":    result.Duration,
			"publisher_user_id": token.UserID,
		}
		if bundleURL != "" {
			updates["bundle_url"] = bundleURL
			updates["bundle_path"] = nil
		} else {
			updates["bundle_url"] = nil
			updates["bundle_path"] = bundlePath
		}

		// Every version requires manual review (like App Store)
		updates["status"] = "pending_review"

		if themeJSON != nil {
			updates["theme_json"] = *themeJSON
		}
		if toolbarJSON != nil {
			updates["toolbar_json"] = *toolbarJSON
		}

		database.DB.Model(&space).Updates(updates)
	}

	// Append-only audit row. Captures owner snapshot at publish time so
	// post-transfer history reads still attribute the right identity.
	// Failure here is logged but doesn't fail the publish — the bundle
	// is already live; missing one history row is preferable to a
	// partial-success state where the user has to retry the upload.
	var bundleURLAudit, bundlePathAudit *string
	if bundleURL != "" {
		s := bundleURL
		bundleURLAudit = &s
	} else if bundlePath != "" {
		s := bundlePath
		bundlePathAudit = &s
	}
	publishRow := models.SpacePublish{
		SpaceID:         space.ID,
		Version:         version,
		PublisherUserID: token.UserID,
		OwnerUserID:     space.OwnerUserID,
		OwnerOrgID:      space.OwnerOrgID,
		SourcePath:      &sourceDest,
		BundleURL:       bundleURLAudit,
		BundlePath:      bundlePathAudit,
		BuildSize:       result.Size,
		BuildDuration:   &result.Duration,
		BuildChecksum:   &result.Checksum,
		PublishedAt:     time.Now(),
	}
	if err := database.DB.Create(&publishRow).Error; err != nil {
		log.Printf("[publish] audit row insert failed: %v", err)
	}

	log.Printf("[publish] %s v%s built successfully by %s (%s) in %s",
		spaceName, version, token.Name, token.UserID, result.Duration)

	WriteJSON(w, 200, map[string]any{
		"status": "pending_review",
		"space": map[string]any{
			"id":      spaceName,
			"version": version,
			"status":  "pending_review",
		},
		"build": map[string]any{
			"checksum": result.Checksum,
			"size":     result.Size,
			"duration": result.Duration,
		},
	})
}

// GET /api/downloads/{name}/{version}/bundle.tar.gz — serve built bundle.
// Redirects to CDN if available, serves locally otherwise.
func DownloadBundle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	version := r.PathValue("version")

	if name == "" || version == "" {
		WriteJSON(w, 400, map[string]any{"error": "Name and version required"})
		return
	}

	var space models.Space
	if err := database.DB.Where("name = ?", name).First(&space).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	if space.Status != "approved" {
		WriteJSON(w, 403, map[string]any{"error": "Space is not approved"})
		return
	}

	// Increment download count
	database.DB.Model(&space).UpdateColumn("downloads", space.Downloads+1)

	// Try R2 first — stream directly from Cloudflare R2
	r2, err := storage.NewR2FromEnv()
	if err == nil {
		key := storage.BundleKey(name, version)
		obj, err := r2.GetObject(key)
		if err == nil && obj.Body != nil {
			defer obj.Body.Close()
			w.Header().Set("Content-Type", "application/gzip")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s-%s.tar.gz", name, version))
			if obj.ContentLength != nil {
				w.Header().Set("Content-Length", fmt.Sprintf("%d", *obj.ContentLength))
			}
			_, _ = io.Copy(w, obj.Body)
			return
		}
	}

	// Fallback: serve from local filesystem
	bundlePath := filepath.Join("data", "bundles", name, version, "bundle.tar.gz")
	if _, err := os.Stat(bundlePath); os.IsNotExist(err) {
		WriteJSON(w, 404, map[string]any{"error": "Bundle not found. Re-publish to upload."})
		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s-%s.tar.gz", name, version))
	http.ServeFile(w, r, bundlePath)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
