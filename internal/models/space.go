package models

import (
	"encoding/json"
	"strings"
	"time"
)

type Space struct {
	ID              uint    `gorm:"primaryKey" json:"-"`
	Name            string  `gorm:"size:100;uniqueIndex;not null" json:"id"`
	DisplayName     string  `gorm:"size:200;not null" json:"name"`
	Description     string  `gorm:"type:text;not null" json:"description"`
	Icon            string  `gorm:"size:200;not null" json:"icon"`
	Version         string  `gorm:"size:50;not null" json:"version"`
	// ScopesJSON stores the surfaces the space is published on as a JSON
	// array of strings ("app" | "org"). Single source of truth for
	// scope info — the legacy single `Scope` column was dropped in the
	// scopes-migration and is backfilled into this field on first boot
	// after the migration ships.
	// No DB-level default: MySQL/MariaDB reject defaults on TEXT columns
	// ("Error 1101 ... can't have a default value"). The default is
	// applied in code: Scopes() returns ["app"] for empty/NULL rows,
	// every write path (handlers + seed) sets ScopesJSON explicitly, and
	// the legacy-scope backfill in main.go covers pre-existing NULL rows.
	ScopesJSON      string  `gorm:"column:scopes_json;type:text" json:"-"`
	// ProjectAware is the orthogonal flag for project-context behaviour.
	// Independent of which surfaces (`scopes`) the space exposes itself on.
	ProjectAware    bool    `gorm:"column:project_aware;default:false" json:"-"`
	// Visibility — who can see this space once it's promoted to the
	// marketplace catalog. "public" (default) is the open channel;
	// "org" restricts the listing to members of OwnerOrgID. Oracle staff
	// review applies to both — visibility only governs catalog reach.
	Visibility      string  `gorm:"size:20;not null;default:'public'" json:"visibility"`
	Category        string  `gorm:"size:50;default:other" json:"category"`
	Author          string  `gorm:"size:200;not null" json:"author"`
	Recommended     bool    `gorm:"default:false" json:"recommended"`
	HostAPIVersion  string  `gorm:"column:host_api_version;size:50;default:^0.2.0" json:"hostApiVersion"`
	BundleURL       *string `gorm:"type:text" json:"bundleUrl,omitempty"`
	Checksum        *string `gorm:"size:200" json:"checksum,omitempty"`
	Downloads       int     `gorm:"default:0" json:"downloads"`
	ScreenshotURL   *string `gorm:"type:text" json:"screenshotUrl,omitempty"`
	PublisherName   *string `gorm:"size:100" json:"publisher,omitempty"`
	HasAgent        bool    `gorm:"default:false" json:"-"`
	SkillCount      int     `gorm:"default:0" json:"-"`
	Status          string  `gorm:"size:20;default:draft" json:"status"`
	RepoURL         *string `gorm:"type:text" json:"repoUrl,omitempty"`
	SubmittedBy     *string `gorm:"size:100" json:"submittedBy,omitempty"`
	PublisherUserID *string `gorm:"size:100" json:"publisherUserId,omitempty"`
	// Ownership — current owner; mutable via SpaceTransfer handshake
	OwnerUserID    *string    `gorm:"size:100;index" json:"ownerUserId,omitempty"`
	OwnerOrgID     *string    `gorm:"size:100;index" json:"ownerOrgId,omitempty"`
	ReviewerNotes  *string    `gorm:"type:text" json:"reviewerNotes,omitempty"`
	ReviewedAt     *time.Time `json:"reviewedAt,omitempty"`
	SourcePath     *string    `gorm:"type:text" json:"-"`
	BundlePath     *string    `gorm:"type:text" json:"-"`
	BuildLog       *string    `gorm:"type:text" json:"-"`
	BuildChecksum  *string    `gorm:"size:200" json:"buildChecksum,omitempty"`
	BuildSize      int64      `gorm:"default:0" json:"-"`
	BuildDuration  *string    `gorm:"size:50" json:"-"`
	NavigationJSON string     `gorm:"column:navigation_json;type:text;not null" json:"-"`
	PagesJSON      string     `gorm:"column:pages_json;type:text;not null" json:"-"`
	ThemeJSON      *string    `gorm:"column:theme_json;type:text" json:"-"`
	ToolbarJSON    *string    `gorm:"column:toolbar_json;type:text" json:"-"`
	CreatedAt      *time.Time `json:"-"`
	UpdatedAt      *time.Time `json:"-"`
}

func (Space) TableName() string { return "spaces" }

// Scopes parses ScopesJSON into a slice. Returns ["app"] when the column
// is empty or unparseable so downstream code never has to deal with nil.
func (s *Space) Scopes() []string {
	out := []string{}
	raw := strings.TrimSpace(s.ScopesJSON)
	if raw == "" {
		return []string{"app"}
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || len(out) == 0 {
		return []string{"app"}
	}
	return out
}

// SetScopes serialises a scopes slice into ScopesJSON. Pass nil/empty to
// reset to the default ["app"].
func (s *Space) SetScopes(values []string) {
	if len(values) == 0 {
		s.ScopesJSON = `["app"]`
		return
	}
	b, _ := json.Marshal(values)
	s.ScopesJSON = string(b)
}

// ToJSON returns the public API representation
func (s *Space) ToJSON() map[string]any {
	result := map[string]any{
		"id":             s.Name,
		"name":           s.DisplayName,
		"description":    s.Description,
		"icon":           s.Icon,
		"version":        s.Version,
		"scopes":         s.Scopes(),
		"projectAware":   s.ProjectAware,
		"author":         s.Author,
		"recommended":    s.Recommended,
		"hostApiVersion": s.HostAPIVersion,
		"status":         s.Status,
		"brain": map[string]any{
			"has_agent":   s.HasAgent,
			"skill_count": s.SkillCount,
		},
		"navigation": jsonParse(s.NavigationJSON),
		"pages":      jsonParse(s.PagesJSON),
	}

	if s.BundleURL != nil {
		result["bundleUrl"] = *s.BundleURL
	}
	if s.Checksum != nil {
		result["checksum"] = *s.Checksum
	}
	if s.Downloads > 0 {
		result["downloads"] = s.Downloads
	}
	if s.ScreenshotURL != nil {
		result["screenshotUrl"] = *s.ScreenshotURL
	}
	if s.PublisherName != nil {
		result["publisher"] = *s.PublisherName
	}
	if s.RepoURL != nil {
		result["repoUrl"] = *s.RepoURL
	}
	if s.SubmittedBy != nil {
		result["submittedBy"] = *s.SubmittedBy
	}
	if s.OwnerUserID != nil {
		result["ownerUserId"] = *s.OwnerUserID
	}
	if s.OwnerOrgID != nil {
		result["ownerOrgId"] = *s.OwnerOrgID
	}
	if s.ReviewerNotes != nil {
		result["reviewerNotes"] = *s.ReviewerNotes
	}
	if s.ReviewedAt != nil {
		result["reviewedAt"] = s.ReviewedAt.Format(time.RFC3339)
	}
	if s.CreatedAt != nil {
		result["createdAt"] = s.CreatedAt.Format(time.RFC3339)
	}
	if s.UpdatedAt != nil {
		result["updatedAt"] = s.UpdatedAt.Format(time.RFC3339)
	}
	if s.ThemeJSON != nil {
		result["theme"] = jsonParse(*s.ThemeJSON)
	}
	if s.ToolbarJSON != nil {
		result["toolbar"] = jsonParse(*s.ToolbarJSON)
	}

	return result
}

// ToRegistryJSON returns the format consumed by the Construct app
func (s *Space) ToRegistryJSON() map[string]any {
	result := map[string]any{
		"id":             s.Name,
		"name":           s.DisplayName,
		"description":    s.Description,
		"icon":           s.Icon,
		"version":        s.Version,
		"scopes":         s.Scopes(),
		"projectAware":   s.ProjectAware,
		"author":         s.Author,
		"recommended":    s.Recommended,
		"hostApiVersion": s.HostAPIVersion,
		"brain": map[string]any{
			"has_agent":   s.HasAgent,
			"skill_count": s.SkillCount,
		},
		"navigation": jsonParse(s.NavigationJSON),
		"pages":      jsonParse(s.PagesJSON),
	}

	if s.ThemeJSON != nil {
		result["theme"] = jsonParse(*s.ThemeJSON)
	}
	if s.ToolbarJSON != nil {
		result["toolbar"] = jsonParse(*s.ToolbarJSON)
	}
	if bundle := s.BundleDownloadURL(""); bundle != "" {
		if isAbsoluteURL(bundle) {
			result["tarball"] = bundle
		}
		result["downloads"] = map[string]any{
			"bundle":   bundle,
			"checksum": s.BundleChecksum(),
		}
	}

	return result
}

// BundleDownloadURL returns the externally reachable bundle URL.
// Older rows may have an R2/CDN URL stored in bundle_path; treat those as URLs
// so the registry does not synthesize a stale APP_URL download link over them.
func (s *Space) BundleDownloadURL(appURL string) string {
	if bundleURL := cleanPtr(s.BundleURL); bundleURL != "" {
		return bundleURL
	}
	if bundlePath := cleanPtr(s.BundlePath); bundlePath != "" {
		if isAbsoluteURL(bundlePath) {
			return bundlePath
		}
		if appURL == "" {
			return downloadPath(s.Name, s.Version)
		}
		return downloadURL(appURL, s.Name, s.Version)
	}
	return ""
}

func (s *Space) BundleChecksum() *string {
	if s.BuildChecksum != nil && strings.TrimSpace(*s.BuildChecksum) != "" {
		return s.BuildChecksum
	}
	return s.Checksum
}

func cleanPtr(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func isAbsoluteURL(value string) bool {
	return strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://")
}

func downloadPath(name, version string) string {
	return "/api/downloads/" + name + "/" + version + "/bundle.tar.gz"
}

func downloadURL(appURL, name, version string) string {
	base := strings.TrimRight(appURL, "/")
	if strings.HasSuffix(base, "/api/developer") {
		return base + strings.TrimPrefix(downloadPath(name, version), "/api")
	}
	return base + downloadPath(name, version)
}

// Pagination wraps a paginated list response.
type Pagination[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
	Page  int `json:"page"`
	Limit int `json:"limit"`
	Pages int `json:"pages"`
}

func NewPagination[T any](items []T, total, page, limit int) Pagination[T] {
	if items == nil {
		items = []T{}
	}
	pages := max((total+limit-1)/limit, 1)
	return Pagination[T]{
		Items: items,
		Total: total,
		Page:  page,
		Limit: limit,
		Pages: pages,
	}
}

// SpaceListItem is the public API shape for a space in listings.
type SpaceListItem struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Icon           string   `json:"icon"`
	Version        string   `json:"version"`
	Scopes         []string `json:"scopes"`
	ProjectAware   bool     `json:"projectAware"`
	Category       string   `json:"category"`
	Author         string   `json:"author"`
	Recommended    bool     `json:"recommended"`
	HostAPIVersion string   `json:"hostApiVersion"`
	Downloads      int      `json:"downloads"`
}

func (s *Space) ToListItem() SpaceListItem {
	return SpaceListItem{
		ID:             s.Name,
		Name:           s.DisplayName,
		Description:    s.Description,
		Icon:           s.Icon,
		Version:        s.Version,
		Scopes:         s.Scopes(),
		ProjectAware:   s.ProjectAware,
		Category:       s.Category,
		Author:         s.Author,
		Recommended:    s.Recommended,
		HostAPIVersion: s.HostAPIVersion,
		Downloads:      s.Downloads,
	}
}

func jsonParse(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}
