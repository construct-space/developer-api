package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"construct/dev-portal/internal/config"
	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
)

var Cfg *config.Config

func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func parseBody(r *http.Request) (map[string]any, error) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body, nil
}

func parseCookie(cookieHeader, name string) string {
	for _, part := range strings.Split(cookieHeader, ";") {
		trimmed := strings.TrimSpace(part)
		if idx := strings.Index(trimmed, "="); idx > 0 {
			if trimmed[:idx] == name {
				return trimmed[idx+1:]
			}
		}
	}
	return ""
}

func getSession(r *http.Request) *models.Session {
	// Gateway-trusted identity wins: when my.lisaos.dev forwards
	// a request with X-Internal-Secret + X-Auth-*, we synthesize a
	// session-shaped view so every handler that already reads from
	// getSession(r).UserID / .Email works unchanged. The returned
	// Session is not persisted — we never write this to the DB.
	if id := gatewayIdentity(r); id != nil {
		uid := id.UserID
		email := id.Email
		name := id.Name
		return &models.Session{
			UserID: &uid,
			Email:  strPtr(email),
			Name:   strPtr(name),
		}
	}

	token := parseCookie(r.Header.Get("Cookie"), "session")
	if token == "" {
		return nil
	}
	var session models.Session
	if err := database.DB.Where("token = ?", token).First(&session).Error; err != nil {
		return nil
	}
	if session.IsExpired() {
		return nil
	}
	return &session
}

// requireSession checks for a valid session and redirects to /login if not found.
// Returns the session if valid, or nil if a redirect was sent.
func requireSession(w http.ResponseWriter, r *http.Request) *models.Session {
	session := getSession(r)
	if session == nil {
		http.Redirect(w, r, "/login?redirect="+r.URL.Path, http.StatusFound)
		return nil
	}
	return session
}

// sessionUser returns a template-friendly map of user data from a session.
func sessionUser(s *models.Session) map[string]any {
	u := map[string]any{}
	if s.UserID != nil {
		u["id"] = *s.UserID
	}
	if s.Name != nil {
		u["name"] = *s.Name
	}
	if s.Email != nil {
		u["email"] = *s.Email
	}
	if s.AvatarURL != nil {
		u["avatar_url"] = *s.AvatarURL
	}
	return u
}

// mySpaces returns all spaces belonging to the given user ID.
func mySpaces(userID string) []models.Space {
	var spaces []models.Space
	database.DB.Where("submitted_by = ?", userID).Order("updated_at DESC").Find(&spaces)
	return spaces
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
