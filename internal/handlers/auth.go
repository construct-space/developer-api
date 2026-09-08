package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
)

var (
	pendingStates   = map[string]time.Time{}
	pendingStatesMu sync.Mutex
)

func generateState() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// GET /api/auth/login — redirect to OAuth provider
func AuthLogin(w http.ResponseWriter, r *http.Request) {
	state := generateState()

	pendingStatesMu.Lock()
	pendingStates[state] = time.Now().Add(10 * time.Minute)
	// Clean expired
	now := time.Now()
	for k, v := range pendingStates {
		if now.After(v) {
			delete(pendingStates, k)
		}
	}
	pendingStatesMu.Unlock()

	params := url.Values{
		"client_id":     {Cfg.OAuthClientID},
		"redirect_uri":  {Cfg.OAuthRedirectURI},
		"response_type": {"code"},
		"scope":         {Cfg.OAuthScope},
		"state":         {state},
	}

	authorizeURL := Cfg.OAuthAuthorize + "?" + params.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

// GET /api/auth/callback — handle OAuth callback
func AuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	oauthErr := r.URL.Query().Get("error")

	if oauthErr != "" {
		redirectWithError(w, r, "OAuth error: "+oauthErr)
		return
	}

	if code == "" {
		redirectWithError(w, r, "Missing authorization code")
		return
	}

	// Validate state
	pendingStatesMu.Lock()
	expiry, ok := pendingStates[state]
	if ok {
		delete(pendingStates, state)
	}
	pendingStatesMu.Unlock()

	if !ok {
		redirectWithError(w, r, "Invalid state parameter")
		return
	}
	if time.Now().After(expiry) {
		redirectWithError(w, r, "State expired")
		return
	}

	// Exchange code for token
	tokenResp, err := exchangeCode(code)
	if err != nil {
		log.Printf("[auth] Token exchange error: %v", err)
		redirectWithError(w, r, "Failed to exchange authorization code")
		return
	}

	accessToken, _ := tokenResp["access_token"].(string)
	if accessToken == "" {
		redirectWithError(w, r, "No access token received")
		return
	}

	// Fetch user info
	userInfo, err := fetchUserInfo(accessToken)
	if err != nil {
		redirectWithError(w, r, "Failed to fetch user profile")
		return
	}

	// Create session
	sessionToken := models.GenerateSessionToken()
	userAgent := r.Header.Get("User-Agent")
	ipAddress := r.Header.Get("X-Forwarded-For")
	if ipAddress != "" {
		ipAddress = strings.Split(ipAddress, ",")[0]
		ipAddress = strings.TrimSpace(ipAddress)
	} else {
		ipAddress = r.Header.Get("X-Real-IP")
		if ipAddress == "" {
			ipAddress = r.RemoteAddr
		}
	}

	userID := fmt.Sprintf("%v", userInfo["id"])
	email, _ := userInfo["email"].(string)
	firstName, _ := userInfo["first_name"].(string)
	lastName, _ := userInfo["last_name"].(string)
	name, _ := userInfo["name"].(string)
	if name == "" {
		name = strings.TrimSpace(firstName + " " + lastName)
	}
	avatarURL, _ := userInfo["avatar_url"].(string)

	expiresAt := time.Now().Add(30 * 24 * time.Hour)
	now := time.Now()
	session := models.Session{
		Token:       sessionToken,
		AccessToken: &accessToken,
		UserID:      strPtr(userID),
		Email:       strPtr(email),
		Name:        strPtr(name),
		AvatarURL:   strPtr(avatarURL),
		UserAgent:   strPtr(userAgent),
		IPAddress:   strPtr(ipAddress),
		ExpiresAt:   &expiresAt,
		CreatedAt:   &now,
	}
	database.DB.Create(&session)

	// Clean expired sessions
	database.DB.Where("expires_at < ?", time.Now()).Delete(&models.Session{})

	cookie := fmt.Sprintf("session=%s; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=%d", sessionToken, 30*24*60*60)
	w.Header().Set("Set-Cookie", cookie)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, Cfg.AppURL, http.StatusFound)
}

// GET /api/auth/me
func AuthMe(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	if session == nil {
		WriteJSON(w, 401, map[string]any{"authenticated": false})
		return
	}

	userData := map[string]any{
		"id":      session.ID,
		"user_id": session.UserID,
	}
	if session.Email != nil {
		userData["email"] = *session.Email
	}
	if session.Name != nil {
		userData["name"] = *session.Name
	}
	if session.AvatarURL != nil {
		userData["avatar_url"] = *session.AvatarURL
	}
	if session.ExpiresAt != nil {
		userData["expires_at"] = session.ExpiresAt.Format(time.RFC3339)
	}
	if session.CreatedAt != nil {
		userData["created_at"] = session.CreatedAt.Format(time.RFC3339)
	}

	WriteJSON(w, 200, map[string]any{
		"authenticated": true,
		"user":          userData,
	})
}

// GET/POST /api/auth/logout
func AuthLogout(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	if session != nil {
		database.DB.Delete(session)
	}
	w.Header().Set("Set-Cookie", "session=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0")
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, Cfg.AppURL, http.StatusFound)
}

// GET /api/auth/sessions
func AuthSessions(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	if session == nil {
		WriteJSON(w, 401, map[string]any{"error": "Unauthorized"})
		return
	}

	if session.UserID == nil {
		WriteJSON(w, 200, map[string]any{"data": []any{}})
		return
	}

	var sessions []models.Session
	database.DB.Where("user_id = ? AND expires_at > ?", *session.UserID, time.Now()).Find(&sessions)

	currentToken := parseCookie(r.Header.Get("Cookie"), "session")
	var result []map[string]any
	for _, s := range sessions {
		data := map[string]any{
			"id":         s.ID,
			"is_current": s.Token == currentToken,
		}
		if s.UserAgent != nil {
			data["user_agent"] = *s.UserAgent
		}
		if s.IPAddress != nil {
			data["ip_address"] = *s.IPAddress
		}
		if s.ExpiresAt != nil {
			data["expires_at"] = s.ExpiresAt.Format(time.RFC3339)
		}
		if s.CreatedAt != nil {
			data["created_at"] = s.CreatedAt.Format(time.RFC3339)
		}
		result = append(result, data)
	}

	WriteJSON(w, 200, map[string]any{"data": result})
}

// DELETE /api/auth/sessions/{id}
func AuthDeleteSession(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	if session == nil {
		WriteJSON(w, 401, map[string]any{"error": "Unauthorized"})
		return
	}

	targetID := r.PathValue("id")
	var target models.Session
	if err := database.DB.First(&target, targetID).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Session not found"})
		return
	}

	if target.UserID == nil || session.UserID == nil || *target.UserID != *session.UserID {
		WriteJSON(w, 403, map[string]any{"error": "Forbidden"})
		return
	}

	database.DB.Delete(&target)
	WriteJSON(w, 200, map[string]any{"message": "Session revoked"})
}

func exchangeCode(code string) (map[string]any, error) {
	body := map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"client_id":     Cfg.OAuthClientID,
		"client_secret": Cfg.OAuthSecret,
		"redirect_uri":  Cfg.OAuthRedirectURI,
	}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", Cfg.OAuthToken, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("token exchange failed: %d %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func fetchUserInfo(accessToken string) (map[string]any, error) {
	req, err := http.NewRequest("GET", Cfg.OAuthUserInfo, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("user info failed: %d %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func redirectWithError(w http.ResponseWriter, r *http.Request, message string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, Cfg.AppURL+"/login?error="+url.QueryEscape(message), http.StatusFound)
}
