package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
)

// GET /api/registry — main endpoint consumed by Construct app.
// Serves directly from the database (source of truth for self-hosted publishing).
func Registry(w http.ResponseWriter, r *http.Request) {
	registryFromDB(w)
}

// registryFromDB serves the registry from the database.
func registryFromDB(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "public, max-age=60")
	var spaces []models.Space
	database.DB.Where("status = ?", "approved").Order("`recommended` DESC").Find(&spaces)

	var items []map[string]any
	for _, s := range spaces {
		item := s.ToRegistryJSON()
		tarball := s.BundleDownloadURL(Cfg.AppURL)
		if tarball == "" {
			continue // skip spaces without builds
		}
		item["tarball"] = tarball
		items = append(items, item)
	}

	WriteJSON(w, 200, map[string]any{
		"version": 1,
		"spaces":  items,
	})
}

// GET /api/categories — returns distinct categories and scopes from approved spaces.
func ListCategories(w http.ResponseWriter, r *http.Request) {
	var categories []string
	database.DB.Model(&models.Space{}).
		Where("status = ? AND category != ''", "approved").
		Distinct("category").
		Order("category").
		Pluck("category", &categories)

	// scopes_json is a JSON array column — distinct surface values can't be
	// aggregated by GROUP BY across DB engines portably, so flatten in Go
	// over the approved set. This list is small (dozens of spaces, two
	// allowed values) so the cost is negligible.
	var rows []models.Space
	database.DB.Model(&models.Space{}).
		Select("scopes_json").
		Where("status = ?", "approved").
		Find(&rows)
	scopeSet := map[string]struct{}{}
	for _, s := range rows {
		for _, v := range s.Scopes() {
			scopeSet[v] = struct{}{}
		}
	}
	scopes := make([]string, 0, len(scopeSet))
	for v := range scopeSet {
		scopes = append(scopes, v)
	}
	sort.Strings(scopes)

	WriteJSON(w, 200, map[string]any{
		"categories": categories,
		"scopes":     scopes,
	})
}

// GET /api/spaces — public listing with search, filter, and pagination.
// Query params: q, scope, recommended, page (1-based, default 1), limit (default 24, max 100)
func ListSpaces(w http.ResponseWriter, r *http.Request) {
	query := strings.ToLower(r.URL.Query().Get("q"))
	scope := r.URL.Query().Get("scope")
	category := r.URL.Query().Get("category")
	recommended := r.URL.Query().Get("recommended")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 24
	}

	db := database.DB.Where("status = ?", "approved")

	if query != "" {
		db = db.Where("LOWER(display_name) LIKE ? OR LOWER(name) LIKE ? OR LOWER(description) LIKE ?",
			"%"+query+"%", "%"+query+"%", "%"+query+"%")
	}
	if recommended == "true" {
		db = db.Where("recommended = ?", true)
	}
	if scope != "" && scope != "all" && (scope == "app" || scope == "org") {
		// scopes_json is a JSON array — substring match on the quoted value
		// covers both single-element and multi-element arrays portably across
		// Postgres + MySQL without resorting to engine-specific JSON ops.
		db = db.Where("scopes_json LIKE ?", "%\""+scope+"\"%")
	}
	if category != "" && category != "all" {
		db = db.Where("category = ?", category)
	}

	var total int64
	db.Model(&models.Space{}).Count(&total)

	var spaces []models.Space
	db.Order("`recommended` DESC, `downloads` DESC").
		Offset((page - 1) * limit).
		Limit(limit).
		Find(&spaces)

	items := make([]models.SpaceListItem, len(spaces))
	for i, s := range spaces {
		items[i] = s.ToListItem()
	}

	WriteJSON(w, 200, models.NewPagination(items, int(total), page, limit))
}

// GET /api/spaces/mine — list spaces created by current user, plus org-owned
// spaces when the caller can manage the active org publisher.
func ListMySpaces(w http.ResponseWriter, r *http.Request) {
	caller := resolveCaller(r)
	if caller == nil {
		WriteJSON(w, 401, map[string]any{"error": "Unauthorized"})
		return
	}

	var spaces []models.Space
	q := database.DB.Where("submitted_by = ?", caller.UserID)
	if caller.CanManageOrgPublisher && caller.OrgID != "" {
		q = q.Or("owner_org_id = ?", caller.OrgID)
	}
	q.Order("created_at DESC").Find(&spaces)

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

// GET /api/spaces/{name} — single space detail
func GetSpace(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		WriteJSON(w, 400, map[string]any{"error": "Name required"})
		return
	}

	// "mine" is a reserved route handled separately
	if name == "mine" {
		ListMySpaces(w, r)
		return
	}

	var space models.Space
	if err := database.DB.Where("name = ?", name).First(&space).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Not found"})
		return
	}

	// Allow owners to see their own spaces
	session := getSession(r)
	isOwner := session != nil && session.UserID != nil && space.SubmittedBy != nil && *session.UserID == *space.SubmittedBy

	if space.Status != "approved" && !isOwner {
		WriteJSON(w, 404, map[string]any{"error": "Not found"})
		return
	}

	WriteJSON(w, 200, map[string]any{"space": space.ToJSON()})
}

// POST /api/spaces — create a draft space
func CreateSpace(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	if session == nil {
		WriteJSON(w, 401, map[string]any{"error": "Unauthorized"})
		return
	}

	body, err := parseBody(r)
	if err != nil {
		WriteJSON(w, 400, map[string]any{"error": "Invalid JSON"})
		return
	}

	name, _ := body["name"].(string)
	displayName, _ := body["displayName"].(string)

	// The `name` column is the slug — it lives in URLs, tarball paths,
	// registry lookups, and filesystem dirs. Keep it URL-safe so every
	// path downstream stays readable. If the client didn't provide a
	// slug, derive one from displayName so the common "just pass a
	// pretty title" flow keeps working.
	name = strings.TrimSpace(name)
	if name == "" && displayName != "" {
		name = slugify(displayName)
	}
	if name == "" {
		WriteJSON(w, 400, map[string]any{"error": "Space name is required"})
		return
	}
	if !isValidSlug(name) {
		WriteJSON(w, 400, map[string]any{
			"error": fmt.Sprintf("Space name %q is not a valid slug — use lowercase letters, digits, and hyphens (2–50 chars, no leading/trailing hyphen).", name),
		})
		return
	}

	// Check if name already taken
	var existing models.Space
	if err := database.DB.Where("name = ?", name).First(&existing).Error; err == nil {
		WriteJSON(w, 409, map[string]any{"error": fmt.Sprintf("Space name %q is already taken", name)})
		return
	}

	if displayName == "" {
		displayName = name
	}
	description, _ := body["description"].(string)
	icon, _ := body["icon"].(string)
	if icon == "" {
		icon = "i-lucide-box"
	}
	version, _ := body["version"].(string)
	if version == "" {
		version = "0.1.0"
	}
	scopes := []string{}
	if rawScopes, ok := body["scopes"].([]any); ok {
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

	// Enforce that the caller is enrolled before they can create a draft
	// space, and that org-scope drafts come from someone with the right
	// to publish on the org's behalf. Previously any authenticated user
	// could POST /api/spaces, which let non-developers clutter the table
	// and let any org member start org-attributed drafts they couldn't
	// finish publishing.
	wantsOrgSpace := false
	for _, s := range scopes {
		if s == "org" {
			wantsOrgSpace = true
			break
		}
	}
	var ownerOrgID *string
	if wantsOrgSpace {
		_, orgID, canManage, _, ok := gatewayOrgContext(r)
		if !ok {
			WriteJSON(w, 403, map[string]any{"error": "Switch to an organization context before creating an org-scope space."})
			return
		}
		if !canManage {
			WriteJSON(w, 403, map[string]any{"error": "Owner, Admin, or Developer role required to create spaces for this organization."})
			return
		}
		var orgPub models.Publisher
		if err := database.DB.Where("org_id = ?", orgID).First(&orgPub).Error; err != nil {
			WriteJSON(w, 403, map[string]any{"error": "Organization isn't enrolled as a developer. An Owner or Admin can enroll it from Settings → Developer."})
			return
		}
		ownerOrgID = &orgID
	} else {
		// Personal scope: require the caller to have a personal Publisher row.
		userID := ""
		if session.UserID != nil {
			userID = *session.UserID
		}
		if userID == "" {
			WriteJSON(w, 401, map[string]any{"error": "Unauthorized"})
			return
		}
		var pub models.Publisher
		if err := database.DB.
			Where("user_id = ? AND (org_id IS NULL OR org_id = '')", userID).
			First(&pub).Error; err != nil {
			WriteJSON(w, 403, map[string]any{"error": "Enroll as a developer (Settings → Developer) before creating a space."})
			return
		}
	}
	projectAware, _ := body["projectAware"].(bool)
	authorName, _ := body["author"].(string)
	if authorName == "" && session.Name != nil {
		authorName = *session.Name
	}
	if authorName == "" {
		authorName = "Unknown"
	}
	hostAPIVersion, _ := body["hostApiVersion"].(string)
	if hostAPIVersion == "" {
		hostAPIVersion = "^0.2.0"
	}
	repoURL, _ := body["repo_url"].(string)

	navJSON := "{}"
	if nav, ok := body["navigation"]; ok {
		b, _ := json.Marshal(nav)
		navJSON = string(b)
	}
	pagesJSON := "[]"
	if pages, ok := body["pages"]; ok {
		b, _ := json.Marshal(pages)
		pagesJSON = string(b)
	}
	var themeJSON *string
	if theme, ok := body["theme"]; ok {
		b, _ := json.Marshal(theme)
		s := string(b)
		themeJSON = &s
	}
	var toolbarJSON *string
	if toolbar, ok := body["toolbar"]; ok {
		b, _ := json.Marshal(toolbar)
		s := string(b)
		toolbarJSON = &s
	}

	space := models.Space{
		Name:           name,
		DisplayName:    displayName,
		Description:    description,
		Icon:           icon,
		Version:        version,
		ScopesJSON:     string(scopesJSON),
		ProjectAware:   projectAware,
		Author:         authorName,
		HostAPIVersion: hostAPIVersion,
		Status:         "draft",
		RepoURL:        strPtr(repoURL),
		SubmittedBy:    session.UserID,
		OwnerOrgID:     ownerOrgID,
		NavigationJSON: navJSON,
		PagesJSON:      pagesJSON,
		ThemeJSON:      themeJSON,
		ToolbarJSON:    toolbarJSON,
	}
	database.DB.Create(&space)

	WriteJSON(w, 201, map[string]any{"space": space.ToJSON()})
}

// PUT /api/spaces/{name} — update a space (owner only)
func UpdateSpace(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	if session == nil {
		WriteJSON(w, 401, map[string]any{"error": "Unauthorized"})
		return
	}

	spaceName := r.PathValue("name")
	var space models.Space
	if err := database.DB.Where("name = ?", spaceName).First(&space).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	if space.SubmittedBy == nil || session.UserID == nil || *space.SubmittedBy != *session.UserID {
		WriteJSON(w, 403, map[string]any{"error": "Forbidden"})
		return
	}

	body, err := parseBody(r)
	if err != nil {
		WriteJSON(w, 400, map[string]any{"error": "Invalid JSON"})
		return
	}

	updates := map[string]any{}
	if v, ok := body["displayName"].(string); ok && v != "" {
		updates["display_name"] = v
	}
	if v, ok := body["description"].(string); ok {
		updates["description"] = v
	}
	if v, ok := body["icon"].(string); ok && v != "" {
		updates["icon"] = v
	}
	if v, ok := body["version"].(string); ok && v != "" {
		updates["version"] = v
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
	if v, ok := body["repo_url"].(string); ok {
		updates["repo_url"] = v
	}
	if nav, ok := body["navigation"]; ok {
		b, _ := json.Marshal(nav)
		updates["navigation_json"] = string(b)
	}
	if pages, ok := body["pages"]; ok {
		b, _ := json.Marshal(pages)
		updates["pages_json"] = string(b)
	}
	if theme, ok := body["theme"]; ok {
		b, _ := json.Marshal(theme)
		updates["theme_json"] = string(b)
	}

	if len(updates) > 0 {
		database.DB.Model(&space).Updates(updates)
	}

	var updated models.Space
	database.DB.Where("name = ?", spaceName).First(&updated)
	WriteJSON(w, 200, map[string]any{"space": updated.ToJSON()})
}

// DELETE /api/spaces/{name} — delete a space (owner only, draft/rejected only)
func DeleteSpace(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	if session == nil {
		WriteJSON(w, 401, map[string]any{"error": "Unauthorized"})
		return
	}

	spaceName := r.PathValue("name")
	var space models.Space
	if err := database.DB.Where("name = ?", spaceName).First(&space).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	if space.SubmittedBy == nil || session.UserID == nil || *space.SubmittedBy != *session.UserID {
		WriteJSON(w, 403, map[string]any{"error": "Forbidden"})
		return
	}

	if space.Status == "approved" {
		WriteJSON(w, 422, map[string]any{"error": "Cannot delete an approved space. Contact support."})
		return
	}

	database.DB.Delete(&space)
	WriteJSON(w, 200, map[string]any{"message": "Space deleted"})
}

// POST /api/spaces/{name}/fetch-manifest — fetch manifest from GitHub repo
func FetchManifest(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	if session == nil {
		WriteJSON(w, 401, map[string]any{"error": "Unauthorized"})
		return
	}

	body, err := parseBody(r)
	if err != nil {
		WriteJSON(w, 400, map[string]any{"error": "Invalid JSON"})
		return
	}

	repoURL, _ := body["repo_url"].(string)
	if repoURL == "" {
		WriteJSON(w, 400, map[string]any{"error": "repo_url is required"})
		return
	}

	manifest, err := fetchManifestFromGitHub(repoURL)
	if err != nil || manifest == nil {
		WriteJSON(w, 422, map[string]any{"error": "Could not fetch space.manifest.json from repository"})
		return
	}

	// Update space if exists and owned by user
	spaceName := r.PathValue("name")
	if spaceName != "" {
		var space models.Space
		if err := database.DB.Where("name = ?", spaceName).First(&space).Error; err == nil {
			if space.SubmittedBy != nil && session.UserID != nil && *space.SubmittedBy == *session.UserID {
				updates := map[string]any{}
				if v, ok := manifest["name"].(string); ok && v != "" {
					updates["display_name"] = v
				}
				if v, ok := manifest["description"].(string); ok {
					updates["description"] = v
				}
				if v, ok := manifest["icon"].(string); ok {
					updates["icon"] = v
				}
				if v, ok := manifest["version"].(string); ok {
					updates["version"] = v
				}
				if rawScopes, ok := manifest["scopes"].([]any); ok {
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
				if v, ok := manifest["projectAware"].(bool); ok {
					updates["project_aware"] = v
				}
				if v, ok := manifest["author"].(string); ok {
					updates["author"] = v
				}
				if v, ok := manifest["hostApiVersion"].(string); ok {
					updates["host_api_version"] = v
				}
				if nav, ok := manifest["navigation"]; ok {
					b, _ := json.Marshal(nav)
					updates["navigation_json"] = string(b)
				}
				if pages, ok := manifest["pages"]; ok {
					b, _ := json.Marshal(pages)
					updates["pages_json"] = string(b)
				}
				if theme, ok := manifest["theme"]; ok {
					b, _ := json.Marshal(theme)
					updates["theme_json"] = string(b)
				}
				updates["repo_url"] = repoURL
				database.DB.Model(&space).Updates(updates)
			}
		}
	}

	WriteJSON(w, 200, map[string]any{"manifest": manifest})
}

// POST /api/spaces/{name}/submit — submit for review
func SubmitSpace(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	if session == nil {
		WriteJSON(w, 401, map[string]any{"error": "Unauthorized"})
		return
	}

	spaceName := r.PathValue("name")
	if spaceName == "" {
		WriteJSON(w, 400, map[string]any{"error": "Space name required"})
		return
	}

	var space models.Space
	if err := database.DB.Where("name = ?", spaceName).First(&space).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	if space.SubmittedBy == nil || session.UserID == nil || *space.SubmittedBy != *session.UserID {
		WriteJSON(w, 403, map[string]any{"error": "Forbidden"})
		return
	}

	if space.Status != "draft" && space.Status != "rejected" {
		WriteJSON(w, 422, map[string]any{"error": "Space cannot be submitted in its current status"})
		return
	}

	database.DB.Model(&space).Updates(map[string]any{
		"status":         "pending_review",
		"reviewer_notes": nil,
		"reviewed_at":    nil,
	})

	var updated models.Space
	database.DB.Where("name = ?", spaceName).First(&updated)
	WriteJSON(w, 200, map[string]any{"space": updated.ToJSON()})
}

// GET /api/spaces/{name}/ownership — return ownership info for a space
func GetSpaceOwnership(w http.ResponseWriter, r *http.Request) {
	spaceName := r.PathValue("name")
	if spaceName == "" {
		WriteJSON(w, 400, map[string]any{"error": "Space name required"})
		return
	}

	var space models.Space
	if err := database.DB.Where("name = ?", spaceName).First(&space).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	ownership := map[string]any{
		"space_id": space.Name,
	}
	if space.OwnerUserID != nil {
		ownership["owner_user_id"] = *space.OwnerUserID
	}
	if space.OwnerOrgID != nil {
		ownership["owner_org_id"] = *space.OwnerOrgID
	}
	if space.CreatedAt != nil {
		ownership["claimed_at"] = space.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
	}

	WriteJSON(w, 200, map[string]any{"ownership": ownership})
}

// POST /api/spaces/{name}/transfer — transfer ownership to a new user.
// Body: { "new_owner_user_id": "..." }
// Requires the current owner's auth token.
func TransferOwnership(w http.ResponseWriter, r *http.Request) {
	token := getCLIToken(r)
	if token == nil {
		// Also accept session auth (web portal)
		session := getSession(r)
		if session == nil || session.UserID == nil {
			WriteJSON(w, 401, map[string]any{"error": "Authentication required"})
			return
		}
		// Use session-based flow
		transferWithUserID(w, r, *session.UserID)
		return
	}
	transferWithUserID(w, r, token.UserID)
}

func transferWithUserID(w http.ResponseWriter, r *http.Request, callerUserID string) {
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

	newOwnerUserID, _ := body["new_owner_user_id"].(string)
	if newOwnerUserID == "" {
		WriteJSON(w, 400, map[string]any{"error": "new_owner_user_id is required"})
		return
	}

	var space models.Space
	if err := database.DB.Where("name = ?", spaceName).First(&space).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	// Verify the caller is the current owner
	if space.OwnerUserID == nil || *space.OwnerUserID != callerUserID {
		WriteJSON(w, 403, map[string]any{"error": "Only the current owner can transfer ownership"})
		return
	}

	// Check the new owner's claim quota
	var newOwnerCount int64
	database.DB.Model(&models.Space{}).Where("owner_user_id = ?", newOwnerUserID).Count(&newOwnerCount)
	if newOwnerCount >= 5 {
		WriteJSON(w, 422, map[string]any{"error": "New owner has reached their claim quota (5 spaces)"})
		return
	}

	// Transfer ownership
	database.DB.Model(&space).Updates(map[string]any{
		"owner_user_id":     newOwnerUserID,
		"publisher_user_id": newOwnerUserID,
	})

	WriteJSON(w, 200, map[string]any{
		"message":           "Ownership transferred",
		"space_id":          space.Name,
		"new_owner_user_id": newOwnerUserID,
	})
}

func fetchManifestFromGitHub(repoURL string) (map[string]any, error) {
	cleaned := strings.TrimSuffix(repoURL, ".git")
	parsed, err := parseGitHubURL(cleaned)
	if err != nil {
		return nil, err
	}

	for _, branch := range []string{"main", "master"} {
		rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/space.manifest.json", parsed.owner, parsed.repo, branch)
		resp, err := http.Get(rawURL)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == 200 {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				continue
			}
			var manifest map[string]any
			if err := json.Unmarshal(body, &manifest); err != nil {
				continue
			}
			return manifest, nil
		}
	}

	return nil, fmt.Errorf("manifest not found")
}

type githubURL struct {
	owner string
	repo  string
}

func parseGitHubURL(rawURL string) (*githubURL, error) {
	u, err := parseURL(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Host != "github.com" {
		return nil, fmt.Errorf("not a GitHub URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid GitHub URL")
	}
	return &githubURL{owner: parts[0], repo: parts[1]}, nil
}

func parseURL(rawURL string) (*urlParts, error) {
	u, err := parseStdURL(rawURL)
	if err != nil {
		return nil, err
	}
	return &urlParts{Host: u.Host, Path: u.Path}, nil
}

type urlParts struct {
	Host string
	Path string
}

func parseStdURL(rawURL string) (*struct{ Host, Path string }, error) {
	// Simple URL parsing
	if !strings.HasPrefix(rawURL, "http") {
		return nil, fmt.Errorf("invalid URL")
	}
	idx := strings.Index(rawURL, "://")
	if idx < 0 {
		return nil, fmt.Errorf("invalid URL")
	}
	rest := rawURL[idx+3:]
	slashIdx := strings.Index(rest, "/")
	if slashIdx < 0 {
		return &struct{ Host, Path string }{Host: rest, Path: "/"}, nil
	}
	return &struct{ Host, Path string }{Host: rest[:slashIdx], Path: rest[slashIdx:]}, nil
}

// Slug rules for the Space.Name column. Chosen because Name ends up in
// URL paths, filesystem dirs (bundle storage), and registry manifests —
// everywhere that rejects spaces and mixed case. Validated on create;
// existing rows that predate this check should be migrated with:
//
//	UPDATE spaces SET name = LOWER(REPLACE(name, ' ', '-'))
//	WHERE name <> LOWER(name) OR name LIKE '% %';
const (
	slugMinLen = 2
	slugMaxLen = 50
)

func isValidSlug(s string) bool {
	if len(s) < slugMinLen || len(s) > slugMaxLen {
		return false
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-':
		default:
			return false
		}
	}
	return true
}

// slugify produces a best-effort slug from a human title. Lowercases,
// swaps runs of non-alphanumeric characters for a single hyphen, and
// trims edge hyphens. "Click Flip" → "click-flip", "Foo — Bar 2.0" →
// "foo-bar-2-0". Callers should still pass the result through
// isValidSlug to catch empty/too-short outputs (e.g. "---" → "").
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	lastDash := true // suppress leading dash
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := b.String()
	return strings.Trim(out, "-")
}
