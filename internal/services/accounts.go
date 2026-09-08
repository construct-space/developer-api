package services

import (
	"bytes"
	"encoding/json"
	"net/http"

	"construct/dev-portal/internal/config"
)

// UserInfo is the minimal user shape returned by accounts /internal/users/batch.
// Used to resolve publisher_user_id → display name for publish-history rows
// when the user has no personal Publisher record on this service (the
// org-publisher path).
type UserInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// BatchUsers resolves a list of user UUIDs against accounts. Returns a map
// keyed by UUID for O(1) lookups by callers. Empty/missing IDs and any
// transport error degrade to an empty map — the caller renders a fallback
// (truncated UUID) so a flaky accounts service never breaks publish-history
// rendering.
func BatchUsers(cfg *config.Config, ids []string) map[string]UserInfo {
	out := map[string]UserInfo{}
	if len(ids) == 0 {
		return out
	}
	if cfg.AccountsURL == "" || cfg.InternalSecret == "" {
		return out
	}
	body, _ := json.Marshal(map[string]any{"ids": ids})
	req, err := http.NewRequest(http.MethodPost, cfg.AccountsURL+"/internal/users/batch", bytes.NewReader(body))
	if err != nil {
		return out
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Secret", cfg.InternalSecret)

	resp, err := client.Do(req)
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out
	}
	var data struct {
		Users []UserInfo `json:"users"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return out
	}
	for _, u := range data.Users {
		if u.ID != "" {
			out[u.ID] = u
		}
	}
	return out
}
