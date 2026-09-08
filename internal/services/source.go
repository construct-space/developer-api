// Package services provides HTTP clients for calling peer infra services
// from developer. All requests include X-Internal-Secret.
package services

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"construct/dev-portal/internal/config"
)

var client = &http.Client{Timeout: 3 * time.Second}

// OwnedOrg is the org-summary shape returned in enrollment responses,
// constructed from a Membership lookup. Field names match the legacy
// /internal/org/owned payload so existing API clients aren't disturbed.
type OwnedOrg struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Icon string `json:"icon"`
}

// Membership mirrors the shape source returns from /internal/membership.
type Membership struct {
	OrgID    string   `json:"org_id"`
	OrgName  string   `json:"org_name"`
	OrgSlug  string   `json:"org_slug"`
	OrgIcon  string   `json:"org_icon"`
	MemberID string   `json:"member_id"`
	Roles    []string `json:"roles"`
	IsOwner  bool     `json:"is_owner"`
}

// CanEnrollOrgAsPublisher returns true when the membership entitles the user
// to enroll their org as a developer publisher: owner, admin, or developer
// role. Kept here (not in handlers) so other call sites — unenroll, future
// "publish as org" gates — share the same definition.
func (m *Membership) CanEnrollOrgAsPublisher() bool {
	if m == nil {
		return false
	}
	if m.IsOwner {
		return true
	}
	for _, r := range m.Roles {
		switch r {
		case "owner", "admin", "developer":
			return true
		}
	}
	return false
}

// GetMembership resolves the user's org membership + role(s). Returns
// (nil, nil) when not in any org. Used by transfer authorization to decide
// whether the caller is an org admin.
func GetMembership(cfg *config.Config, userID string) (*Membership, error) {
	if cfg.SourceURL == "" || cfg.InternalSecret == "" {
		return nil, nil
	}
	u := cfg.SourceURL + "/internal/membership?user_id=" + url.QueryEscape(userID)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("X-Internal-Secret", cfg.InternalSecret)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	var m Membership
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// SeedDeveloperRole tells source to add the Developer role to the org's role
// catalog. Idempotent on the source side. Called on successful org enroll.
func SeedDeveloperRole(cfg *config.Config, orgID string) error {
	return postInternal(cfg, "/internal/developer-role/seed", map[string]any{"org_id": orgID})
}

// UnseedDeveloperRole tells source to remove the Developer role. Called when
// an org un-enrolls as publisher.
func UnseedDeveloperRole(cfg *config.Config, orgID string) error {
	return postInternal(cfg, "/internal/developer-role/unseed", map[string]any{"org_id": orgID})
}

func postInternal(cfg *config.Config, path string, body map[string]any) error {
	if cfg.SourceURL == "" || cfg.InternalSecret == "" {
		return nil
	}
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, cfg.SourceURL+path, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", cfg.InternalSecret)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
