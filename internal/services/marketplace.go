// marketplace.go — HTTP client for marketplace-api.
//
// Called from the admin approve/unpublish handlers (best-effort: a
// marketplace failure must not roll back an approve). The marketplace
// catalog is the consumer-facing read view of approved spaces; promote
// pushes new state, demote soft-removes from the public list.
//
// All calls authenticated with X-Internal-Secret over the swarm overlay
// (srv-captain--marketplace-api). No public-domain hops.
package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"construct/dev-portal/internal/config"
	"construct/dev-portal/internal/models"
)

// PromotePayload mirrors the marketplace-api Space row shape. We send
// everything the catalog needs; marketplace upserts by `id` (slug).
type PromotePayload struct {
	ID            string         `json:"id"`              // slug, primary key on marketplace side
	Name          string         `json:"name"`            // display name
	Description   string         `json:"description"`
	Icon          string         `json:"icon"`
	Version       string         `json:"version"`
	HostAPIVer    string         `json:"host_api_version,omitempty"`
	Manifest      map[string]any `json:"manifest,omitempty"`
	TarballURL    string         `json:"tarball_url,omitempty"`
	Scopes        []string       `json:"scopes,omitempty"`
	ProjectAware  bool           `json:"projectAware,omitempty"`
	// Visibility — "public" (default) or "org". Forwarded as-is from the
	// developer-side row so marketplace listings can scope org-private
	// rows to the owning org's members.
	Visibility    string         `json:"visibility,omitempty"`
	// OwnerOrgID — required by marketplace when Visibility == "org"; the
	// catalog rejects an "org" promotion without it.
	OwnerOrgID    *string        `json:"owner_org_id,omitempty"`
	PublisherSlug string         `json:"publisher_slug,omitempty"`
	PublisherName string         `json:"publisher_name,omitempty"`
	Category      *string        `json:"category,omitempty"`
	Tags          []string       `json:"tags,omitempty"`
}

// PromoteSpace pushes one approved space into the marketplace catalog.
// Idempotent: marketplace upserts by slug, so re-promoting an existing
// row updates manifest/version/etc. without disturbing trending counters.
func PromoteSpace(cfg *config.Config, s *models.Space) error {
	endpoint := strings.TrimRight(cfg.MarketplaceURL, "/") + "/internal/spaces"

	payload := buildPromotePayload(s)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal promote payload: %w", err)
	}

	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", cfg.InternalSecret)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("call marketplace: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("marketplace promote %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// DemoteSpace soft-deletes a space from the marketplace catalog. Called
// from the unpublish handler. Idempotent: 404 from marketplace (already
// gone) is treated as success.
func DemoteSpace(cfg *config.Config, name string) error {
	endpoint := strings.TrimRight(cfg.MarketplaceURL, "/") + "/internal/spaces/" + url.PathEscape(name)

	req, err := http.NewRequest("DELETE", endpoint, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("X-Internal-Secret", cfg.InternalSecret)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("call marketplace: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil // already absent
	}
	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("marketplace demote %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// PromoteSpaceBestEffort wraps PromoteSpace with a log-and-swallow shim
// for use in admin approve/unpublish flows. The user action (approve)
// must succeed even if marketplace is unreachable; we'll fix up via the
// backfill endpoint.
func PromoteSpaceBestEffort(cfg *config.Config, s *models.Space) {
	if err := PromoteSpace(cfg, s); err != nil {
		log.Printf("[marketplace] promote %s failed (best-effort): %v", s.Name, err)
	}
}

// DemoteSpaceBestEffort mirrors PromoteSpaceBestEffort.
func DemoteSpaceBestEffort(cfg *config.Config, name string) {
	if err := DemoteSpace(cfg, name); err != nil {
		log.Printf("[marketplace] demote %s failed (best-effort): %v", name, err)
	}
}

// buildPromotePayload maps developer.models.Space → marketplace payload.
// Manifest is reconstructed from the JSON-string columns developer keeps
// for navigation/pages/theme/toolbar.
func buildPromotePayload(s *models.Space) PromotePayload {
	manifest := map[string]any{
		"id":             s.Name,
		"name":           s.DisplayName,
		"description":    s.Description,
		"icon":           s.Icon,
		"version":        s.Version,
		"scopes":         s.Scopes(),
		"projectAware":   s.ProjectAware,
		"author":         s.Author,
		"hostApiVersion": s.HostAPIVersion,
		"navigation":     jsonParse(s.NavigationJSON),
		"pages":          jsonParse(s.PagesJSON),
	}
	if s.ThemeJSON != nil {
		manifest["theme"] = jsonParse(*s.ThemeJSON)
	}
	if s.ToolbarJSON != nil {
		manifest["toolbar"] = jsonParse(*s.ToolbarJSON)
	}

	pubSlug, pubName := publisherSlugAndName(s)

	var category *string
	if c := strings.TrimSpace(s.Category); c != "" && c != "other" {
		category = &c
	}

	visibility := s.Visibility
	if visibility == "" {
		visibility = "public"
	}

	return PromotePayload{
		ID:            s.Name,
		Name:          s.DisplayName,
		Description:   s.Description,
		Icon:          s.Icon,
		Version:       s.Version,
		HostAPIVer:    s.HostAPIVersion,
		Manifest:      manifest,
		TarballURL:    s.BundleDownloadURL(""),
		Scopes:        s.Scopes(),
		ProjectAware:  s.ProjectAware,
		Visibility:    visibility,
		OwnerOrgID:    s.OwnerOrgID,
		PublisherSlug: pubSlug,
		PublisherName: pubName,
		Category:      category,
	}
}

func publisherSlugAndName(s *models.Space) (slug, name string) {
	if s.PublisherName != nil && strings.TrimSpace(*s.PublisherName) != "" {
		name = *s.PublisherName
	} else {
		name = s.Author
	}
	slug = slugify(name)
	return
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			out = append(out, c)
		case c == ' ' || c == '-' || c == '_':
			if len(out) > 0 && out[len(out)-1] != '-' {
				out = append(out, '-')
			}
		}
	}
	return strings.Trim(string(out), "-")
}

func jsonParse(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil
	}
	return v
}
