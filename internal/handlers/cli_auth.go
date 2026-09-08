package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
	"construct/dev-portal/internal/services"
)

// pendingCLILogins stores callback URLs keyed by OAuth state,
// so after OAuth completes we know where to redirect with the token.
var (
	pendingCLILogins   = map[string]string{} // state -> callbackURL
	pendingCLILoginsMu sync.Mutex
)

func generateCLIState() string {
	b := make([]byte, 32)
	rand.Read(b)
	return "cli_" + hex.EncodeToString(b)
}

// isLoopbackCallback reports whether a CLI callback URL is safe to
// redirect a freshly minted CLI token to. Matches the OAuth 2.0 for
// Native Apps BCP (RFC 8252 §7.3): http scheme + loopback host + any
// port. Rejects anything else so an attacker can't pass
// callback=https://evil.example/steal and get a token redirected there.
func isLoopbackCallback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// GET /api/auth/cli-login — initiate OAuth for CLI.
// Query params:
//   - callback: the localhost URL the CLI is listening on (e.g. http://localhost:12345/callback)
//
// Redirects to accounts.lisaos.dev OAuth, which calls back to /api/auth/cli-callback.
func CLILogin(w http.ResponseWriter, r *http.Request) {
	callbackURL := r.URL.Query().Get("callback")
	if callbackURL == "" {
		WriteJSON(w, 400, map[string]any{"error": "callback parameter required"})
		return
	}
	// RFC 8252 §7.3: native apps MUST use loopback for the redirect URI.
	// Without this check, an attacker can point callback= at an arbitrary
	// host and the service would mint a cst_live_* token and redirect to
	// them with ?token= in the query string — a token leak.
	if !isLoopbackCallback(callbackURL) {
		WriteJSON(w, 400, map[string]any{"error": "callback must be an http loopback URL (127.0.0.1, localhost, or [::1])"})
		return
	}

	state := generateCLIState()

	pendingCLILoginsMu.Lock()
	pendingCLILogins[state] = callbackURL
	// Clean expired entries (older than 10 minutes)
	// We don't have timestamps here, so just limit the map size
	if len(pendingCLILogins) > 100 {
		for k := range pendingCLILogins {
			delete(pendingCLILogins, k)
			break
		}
	}
	pendingCLILoginsMu.Unlock()

	// Use the portal's own OAuth client — the CLI client (construct_cli) has
	// a custom scheme redirect (construct-cli://) which is harder to handle.
	// Instead, the portal acts as middleman: OAuth callback comes here,
	// we create a CLI token, then redirect to the CLI's localhost callback.
	params := url.Values{
		"client_id":     {Cfg.OAuthCLIClientID},
		"redirect_uri":  {Cfg.AppURL + "/api/auth/callback-cli"},
		"response_type": {"code"},
		"scope":         {Cfg.OAuthScope},
		"state":         {state},
	}

	authorizeURL := Cfg.OAuthAuthorize + "?" + params.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

// GET /api/auth/cli-callback — OAuth callback for CLI login.
// After successful OAuth, creates a CLI token and redirects to the CLI's localhost callback.
func CLICallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	oauthErr := r.URL.Query().Get("error")

	if oauthErr != "" {
		renderCLIError(w, "OAuth error: "+oauthErr, state)
		return
	}

	if code == "" || state == "" {
		renderCLIError(w, "Missing code or state", state)
		return
	}

	// Look up the CLI callback URL
	pendingCLILoginsMu.Lock()
	callbackURL, ok := pendingCLILogins[state]
	if ok {
		delete(pendingCLILogins, state)
	}
	pendingCLILoginsMu.Unlock()

	if !ok {
		renderCLIError(w, "Invalid or expired login session", "")
		return
	}

	// Exchange code for access token
	tokenResp, err := exchangeCLICode(code)
	if err != nil {
		renderCLIError(w, "Token exchange failed", "")
		return
	}

	accessToken, _ := tokenResp["access_token"].(string)
	if accessToken == "" {
		renderCLIError(w, "No access token received", "")
		return
	}

	// Fetch user info from accounts
	userInfo, err := fetchCLIUserInfo(accessToken)
	if err != nil {
		renderCLIError(w, "Failed to fetch user info", "")
		return
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

	// Developer enrollment is now determined by the presence of a Publisher
	// row in *this* service rather than a status column on accounts — see
	// /api/enroll/personal and /api/enroll/org. Check by user_id first; fall
	// back to email for legacy rows that haven't been linked yet.
	var publisher models.Publisher
	err = database.DB.
		Where("user_id = ? OR email = ?", userID, email).
		First(&publisher).Error
	if err != nil {
		renderCLIError(w, "Developer enrollment required. Visit lisaos.dev/settings to enroll.", "")
		return
	}

	// Hand the CLI the accounts-issued cat_* directly instead of minting a
	// separate cst_live_* in cli_tokens here. Two token formats meant two
	// validation paths and one of them (cst_live_*) had no way to be
	// revoked without a separate revoke endpoint. cat_* is the canonical
	// identity token; downstream services already resolve it via accounts
	// /api/me/scope, so the rest of the CLI flow keeps working unchanged.
	//
	// Existing cst_live_* tokens issued by older versions of this handler
	// continue to validate (services run go-auth compat mode); they'll
	// age out as users re-login.
	_ = avatarURL // captured for legacy CLIToken row above; no longer stored
	redirect := fmt.Sprintf("%s?token=%s", callbackURL, url.QueryEscape(accessToken))
	http.Redirect(w, r, redirect, http.StatusFound)
}

// GET /api/auth/cli-verify — verify the caller and return their publisher
// identities. Accepts EITHER a CLI Bearer token (original purpose — CLI
// calls this after login) OR a web session cookie (the portal's Profile
// and Keys pages read this to show "are you enrolled?"). Same response
// shape either way so both clients share a single source of truth.
func CLIVerify(w http.ResponseWriter, r *http.Request) {
	var userID, name, email string

	if token := getCLIToken(r); token != nil {
		userID, name, email = token.UserID, token.Name, token.Email
	} else if session := getSession(r); session != nil {
		if session.UserID != nil {
			userID = *session.UserID
		}
		if session.Name != nil {
			name = *session.Name
		}
		if session.Email != nil {
			email = *session.Email
		}
	} else {
		WriteJSON(w, 401, map[string]any{"error": "Invalid or expired token"})
		return
	}

	resp := map[string]any{
		"user": map[string]any{
			"id":    userID,
			"name":  name,
			"email": email,
		},
	}

	// Resolve publisher identity for the caller. Same query whether session
	// or CLI — the Publisher row is keyed by user_id or email regardless
	// of how the caller authenticated. Both portal and CLI surface the
	// result as "which publishers can this person act on behalf of?".
	var publishers []models.Publisher
	database.DB.
		Where("user_id = ? OR email = ?", userID, email).
		Order("CASE WHEN user_id IS NOT NULL THEN 0 ELSE 1 END, created_at DESC").
		Limit(2).
		Find(&publishers)

	orgID := ""
	canManageOrgPublisher := false
	if _, gatewayOrgID, gatewayCanManage, _, ok := gatewayOrgContext(r); ok {
		orgID = gatewayOrgID
		canManageOrgPublisher = gatewayCanManage
	} else if membership, _ := services.GetMembership(Cfg, userID); membership != nil {
		orgID = membership.OrgID
		canManageOrgPublisher = membership.CanEnrollOrgAsPublisher()
	}
	if orgID != "" && canManageOrgPublisher {
		var orgPublisher models.Publisher
		if err := database.DB.Where("org_id = ?", orgID).First(&orgPublisher).Error; err == nil {
			publishers = append(publishers, orgPublisher)
		}
	}

	publisherList := make([]map[string]any, 0, len(publishers))
	seenPublishers := map[uint]bool{}
	for i := range publishers {
		p := &publishers[i]
		if seenPublishers[p.ID] {
			continue
		}
		seenPublishers[p.ID] = true
		publisherList = append(publisherList, map[string]any{
			"name":     p.Name,
			"email":    p.Email,
			"kind":     p.Kind(),
			"userId":   p.UserID,
			"orgId":    p.OrgID,
			"verified": p.Verified,
			"api_key":  p.APIKey,
		})
	}
	resp["publishers"] = publisherList

	WriteJSON(w, 200, resp)
}

// POST /api/auth/cli-revoke — revoke the current CLI token.
func CLIRevoke(w http.ResponseWriter, r *http.Request) {
	token := getCLIToken(r)
	if token == nil {
		WriteJSON(w, 401, map[string]any{"error": "Invalid token"})
		return
	}

	database.DB.Delete(token)
	WriteJSON(w, 200, map[string]any{"message": "Token revoked"})
}

// getCLIToken extracts and validates a CLI token from the Authorization header,
// then overlays publisher attribution from X-API-Key when present.
//
// Authorization shapes:
//   - cst_live_… : dedicated CLI token issued by /api/auth/cli-login, stored
//     locally in cli_tokens.
//   - cat_…      : accounts OAuth token shared with the Construct app. Used
//     when the CLI reads the app's profiles.json instead of doing its own
//     login. Resolved by calling accounts /api/me and synthesizing an
//     ephemeral CLIToken from the response.
//   - csk_live_… : publisher API key as Bearer. Legacy direct-to-developer
//     path — works when this service is hit without the gateway. Through
//     the gateway it 401s at accounts validate-token, so prefer X-API-Key.
//
// X-API-Key (csk_live_…): publisher proof. When the resolved identity is a
// member of the org that owns the publisher, the token's OrgID is set so
// downstream publish logic attributes the artifact to the org.
func getCLIToken(r *http.Request) *models.CLIToken {
	token := resolveIdentityToken(r)
	if token == nil {
		return nil
	}
	overlayPublisherKey(r, token)
	return token
}

func resolveIdentityToken(r *http.Request) *models.CLIToken {
	auth := r.Header.Get("Authorization")
	tokenStr := strings.TrimPrefix(auth, "Bearer ")
	if tokenStr == "" || tokenStr == auth {
		return nil
	}

	if strings.HasPrefix(tokenStr, "cat_") {
		return resolveAccountsToken(tokenStr)
	}

	if strings.HasPrefix(tokenStr, "csk_") {
		return resolvePublisherKey(tokenStr)
	}

	// Must be a CLI token (cst_live_ prefix)
	if !strings.HasPrefix(tokenStr, "cst_live_") {
		return nil
	}

	var token models.CLIToken
	if err := database.DB.Where("token = ?", tokenStr).First(&token).Error; err != nil {
		return nil
	}

	if token.IsExpired() {
		database.DB.Delete(&token)
		return nil
	}

	return &token
}

// overlayPublisherKey reads X-API-Key (csk_live_*) and, if it resolves to an
// org publisher, copies the org id onto the token. The Bearer establishes
// who the caller is; X-API-Key proves which publisher they're acting as.
// When the publisher is org-owned and the key checks out, OrgID is set so
// PublishSpace takes the org-attribution branch.
func overlayPublisherKey(r *http.Request, token *models.CLIToken) {
	apiKey := r.Header.Get("X-API-Key")
	if !strings.HasPrefix(apiKey, "csk_") {
		return
	}
	var pub models.Publisher
	if err := database.DB.Where("api_key = ?", apiKey).First(&pub).Error; err != nil {
		return
	}
	if pub.OrgID != nil && *pub.OrgID != "" {
		token.OrgID = *pub.OrgID
	}
}

// resolvePublisherKey looks up a publisher by its csk_ API key and returns
// a synthetic CLIToken whose UserID/Email match the publisher's owning
// identity. The returned token is not persisted — it just satisfies the
// CLIToken-shaped contract the rest of cli-verify expects. Returns nil
// when the key is unknown.
func resolvePublisherKey(key string) *models.CLIToken {
	var pub models.Publisher
	if err := database.DB.Where("api_key = ?", key).First(&pub).Error; err != nil {
		return nil
	}

	userID := ""
	if pub.UserID != nil {
		userID = *pub.UserID
	}
	orgID := ""
	if pub.OrgID != nil {
		orgID = *pub.OrgID
	}

	return &models.CLIToken{
		UserID: userID,
		OrgID:  orgID,
		Name:   pub.Name,
		Email:  pub.Email,
		Label:  "publisher-key",
	}
}

func renderCLIError(w http.ResponseWriter, message, state string) {
	// Try to redirect error to CLI callback if we have the state
	if state != "" {
		pendingCLILoginsMu.Lock()
		callbackURL, ok := pendingCLILogins[state]
		if ok {
			delete(pendingCLILogins, state)
		}
		pendingCLILoginsMu.Unlock()

		if ok {
			redirect := fmt.Sprintf("%s?error=%s", callbackURL, url.QueryEscape(message))
			http.Redirect(w, nil, redirect, http.StatusFound)
			return
		}
	}

	// Fallback: render error page
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(400)
	fmt.Fprintf(w, `<html><body style="font-family:system-ui;text-align:center;padding:60px">
		<h2 style="color:#EF4444">Login Failed</h2>
		<p>%s</p>
		<p style="color:#6B7280">Return to your terminal and try again.</p>
	</body></html>`, message)
}

func exchangeCLICode(code string) (map[string]any, error) {
	body := map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"client_id":     Cfg.OAuthCLIClientID,
		"client_secret": Cfg.OAuthCLISecret,
		"redirect_uri":  Cfg.AppURL + "/api/auth/callback-cli",
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
		return nil, fmt.Errorf("token exchange failed: %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// resolveAccountsToken maps a shared accounts OAuth token (cat_…) into an
// ephemeral CLIToken-shaped value by calling accounts /api/me. Not persisted
// — returned to handlers that expect CLIToken{UserID, Name, Email}. If the
// caller isn't an enrolled publisher (no Publisher row) this still returns
// the identity; downstream handlers enforce publishing access separately.
func resolveAccountsToken(accessToken string) *models.CLIToken {
	info, err := fetchCLIUserInfo(accessToken)
	if err != nil {
		return nil
	}
	userID := fmt.Sprintf("%v", info["id"])
	if userID == "" || userID == "<nil>" {
		return nil
	}
	email, _ := info["email"].(string)
	firstName, _ := info["first_name"].(string)
	lastName, _ := info["last_name"].(string)
	name, _ := info["name"].(string)
	if name == "" {
		name = strings.TrimSpace(firstName + " " + lastName)
	}
	return &models.CLIToken{
		Token:  accessToken,
		UserID: userID,
		Name:   name,
		Email:  email,
	}
}

func fetchCLIUserInfo(accessToken string) (map[string]any, error) {
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
		return nil, fmt.Errorf("user info failed: %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	return result, nil
}
