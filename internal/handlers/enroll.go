package handlers

import (
	"log"
	"net/http"
	"strings"
	"time"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
	"construct/dev-portal/internal/services"
)

// POST /api/enroll/personal — enroll the currently logged-in user as a
// personal developer. Creates a Publisher row keyed by UserID + Email and
// returns the API key (shown once).
func EnrollPersonal(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	if session == nil || session.UserID == nil || session.Email == nil {
		WriteJSON(w, 401, map[string]any{"error": "Authentication required"})
		return
	}

	userID := *session.UserID
	email := *session.Email

	// Already enrolled by user_id?
	var existing models.Publisher
	if err := database.DB.Where("user_id = ?", userID).First(&existing).Error; err == nil {
		WriteJSON(w, 200, map[string]any{
			"publisher": publisherResponse(&existing),
			"message":   "already enrolled",
		})
		return
	}

	// Legacy Publisher with matching email but no user_id — backfill it
	// rather than failing the insert on the email uniqueness index. This is
	// the migration path for rows created by the old RegisterPublisher flow.
	var legacy models.Publisher
	if err := database.DB.Where("email = ? AND (user_id IS NULL OR user_id = ?)", email, "").First(&legacy).Error; err == nil {
		database.DB.Model(&legacy).Update("user_id", userID)
		legacy.UserID = &userID
		WriteJSON(w, 200, map[string]any{
			"publisher": publisherResponse(&legacy),
			"message":   "linked existing publisher to your account",
		})
		return
	}

	// Derive a publisher name from email local-part; user can rename later.
	name := deriveName(session.Name, email)
	if taken(name) {
		name = uniquify(name)
	}

	apiKey := models.GenerateAPIKey()
	p := models.Publisher{
		Name:   name,
		Email:  email,
		APIKey: apiKey,
		UserID: &userID,
	}
	if err := database.DB.Create(&p).Error; err != nil {
		log.Printf("[enroll/personal] create failed: %v (email=%s user_id=%s name=%s)", err, email, userID, name)
		WriteJSON(w, 500, map[string]any{"error": "failed to enroll", "detail": err.Error()})
		return
	}

	WriteJSON(w, 201, map[string]any{
		"publisher": publisherResponse(&p),
		"apiKey":    apiKey,
		"message":   "Save your API key — it will not be shown again.",
	})
}

// POST /api/enroll/org — enroll an organization as a publisher. Caller must
// hold owner/admin/developer role in the org (verified via source). On
// success, the Developer role is seeded in the org's role catalog so admins
// can assign it to members.
func EnrollOrg(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	if session == nil || session.UserID == nil {
		WriteJSON(w, 401, map[string]any{"error": "Authentication required"})
		return
	}
	userID := *session.UserID

	// Resolve membership and authorize on owner ∪ admin ∪ developer roles.
	// Anyone in those roles can enroll the org — matches the user's mental
	// model that admins manage org integrations, and avoids blocking dev
	// teams when the legal owner isn't a daily-driver of the workspace.
	membership, err := services.GetMembership(Cfg, userID)
	if err != nil {
		log.Printf("[enroll/org] source lookup failed: %v", err)
		WriteJSON(w, 502, map[string]any{"error": "upstream unavailable"})
		return
	}
	if membership == nil {
		WriteJSON(w, 403, map[string]any{"error": "you are not a member of any organization"})
		return
	}
	if !membership.CanEnrollOrgAsPublisher() {
		WriteJSON(w, 403, map[string]any{"error": "only the organization's owner, admin, or developer can enroll it as a publisher"})
		return
	}
	org := &services.OwnedOrg{
		ID:   membership.OrgID,
		Name: membership.OrgName,
		Slug: membership.OrgSlug,
		Icon: membership.OrgIcon,
	}

	// Already enrolled?
	var existing models.Publisher
	if err := database.DB.Where("org_id = ?", org.ID).First(&existing).Error; err == nil {
		WriteJSON(w, 200, map[string]any{
			"publisher": publisherResponse(&existing),
			"org":       org,
			"message":   "already enrolled",
		})
		return
	}

	// Publisher name follows org slug — keeps publisher namespace aligned
	// with org identity. Collision fallback preserves org association.
	name := org.Slug
	if name == "" {
		name = strings.ToLower(org.Name)
	}
	if taken(name) {
		name = uniquify(name)
	}

	// Org publishers don't carry an email — identity is OrgID. A billing/
	// contact email can live on the Organization row or a future OrgBilling
	// table. Leaving it empty avoids sharing the owner's personal email on
	// what's a team-level identity.
	apiKey := models.GenerateAPIKey()
	orgID := org.ID
	p := models.Publisher{
		Name:   name,
		APIKey: apiKey,
		OrgID:  &orgID,
	}
	if err := database.DB.Create(&p).Error; err != nil {
		log.Printf("[enroll/org] create failed: %v", err)
		WriteJSON(w, 500, map[string]any{"error": "failed to enroll org"})
		return
	}

	// Callback to source: seed the Developer role so admins can assign it.
	// Failure here is logged but doesn't roll back — the org is still a
	// publisher; admin can retry role seed from settings.
	if err := services.SeedDeveloperRole(Cfg, org.ID); err != nil {
		log.Printf("[enroll/org] seed developer role failed: %v", err)
	}

	WriteJSON(w, 201, map[string]any{
		"publisher": publisherResponse(&p),
		"org":       org,
		"apiKey":    apiKey,
		"message":   "Save your API key — it will not be shown again.",
	})
}

// DELETE /api/enroll/personal — disable the caller's personal developer
// status. Refuses when spaces are still owned by the caller so we don't
// leave orphan records; user must delete/transfer them first.
func UnenrollPersonal(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	if session == nil || session.UserID == nil {
		WriteJSON(w, 401, map[string]any{"error": "Authentication required"})
		return
	}
	userID := *session.UserID

	var existing models.Publisher
	if err := database.DB.Where("user_id = ?", userID).First(&existing).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "not enrolled"})
		return
	}

	// Safety: don't orphan spaces. OwnerUserID is the authoritative
	// current-owner column (SubmittedBy is historical and can stay).
	var spaceCount int64
	database.DB.Model(&models.Space{}).Where("owner_user_id = ?", userID).Count(&spaceCount)
	if spaceCount > 0 {
		WriteJSON(w, 409, map[string]any{
			"error":       "has_spaces",
			"space_count": spaceCount,
			"message":     "Delete or transfer your spaces before disabling the developer status.",
		})
		return
	}

	if err := database.DB.Delete(&existing).Error; err != nil {
		log.Printf("[unenroll/personal] delete failed: %v", err)
		WriteJSON(w, 500, map[string]any{"error": "failed to disable developer status"})
		return
	}

	WriteJSON(w, 200, map[string]any{"ok": true})
}

// DELETE /api/enroll/org — disable the org's developer status. Caller
// must hold owner/admin/developer role in the org. Refuses when spaces
// are still owned by the org. On success, source is told to unseed the
// Developer role.
func UnenrollOrg(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	if session == nil || session.UserID == nil {
		WriteJSON(w, 401, map[string]any{"error": "Authentication required"})
		return
	}
	userID := *session.UserID

	membership, err := services.GetMembership(Cfg, userID)
	if err != nil {
		log.Printf("[unenroll/org] source lookup failed: %v", err)
		WriteJSON(w, 502, map[string]any{"error": "upstream unavailable"})
		return
	}
	if membership == nil {
		WriteJSON(w, 403, map[string]any{"error": "you are not a member of any organization"})
		return
	}
	if !membership.CanEnrollOrgAsPublisher() {
		WriteJSON(w, 403, map[string]any{"error": "only the organization's owner, admin, or developer can disable developer status"})
		return
	}
	org := &services.OwnedOrg{
		ID:   membership.OrgID,
		Name: membership.OrgName,
		Slug: membership.OrgSlug,
		Icon: membership.OrgIcon,
	}

	var existing models.Publisher
	if err := database.DB.Where("org_id = ?", org.ID).First(&existing).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "not enrolled"})
		return
	}

	var spaceCount int64
	database.DB.Model(&models.Space{}).Where("owner_org_id = ?", org.ID).Count(&spaceCount)
	if spaceCount > 0 {
		WriteJSON(w, 409, map[string]any{
			"error":       "has_spaces",
			"space_count": spaceCount,
			"message":     "Delete or transfer the org's spaces before disabling the developer status.",
		})
		return
	}

	if err := database.DB.Delete(&existing).Error; err != nil {
		log.Printf("[unenroll/org] delete failed: %v", err)
		WriteJSON(w, 500, map[string]any{"error": "failed to disable developer status"})
		return
	}

	// Tell source to remove the Developer role from the org's catalog.
	// Best-effort — log but don't fail the request; role cleanup is
	// cosmetic once the publisher row is gone.
	if err := services.UnseedDeveloperRole(Cfg, org.ID); err != nil {
		log.Printf("[unenroll/org] unseed role failed: %v", err)
	}

	WriteJSON(w, 200, map[string]any{"ok": true})
}

func publisherResponse(p *models.Publisher) map[string]any {
	resp := map[string]any{
		"name":     p.Name,
		"email":    p.Email,
		"kind":     p.Kind(),
		"verified": p.Verified,
	}
	if p.UserID != nil {
		resp["userId"] = *p.UserID
	}
	if p.OrgID != nil {
		resp["orgId"] = *p.OrgID
	}
	if p.CreatedAt != nil {
		resp["createdAt"] = p.CreatedAt.Format(time.RFC3339)
	}
	return resp
}

func deriveName(sessionName *string, email string) string {
	if sessionName != nil {
		n := strings.ToLower(strings.TrimSpace(*sessionName))
		n = strings.ReplaceAll(n, " ", "-")
		if n != "" {
			return n
		}
	}
	if local, _, ok := strings.Cut(email, "@"); ok {
		return strings.ToLower(local)
	}
	return "publisher"
}

func taken(name string) bool {
	var count int64
	database.DB.Model(&models.Publisher{}).Where("name = ?", name).Count(&count)
	return count > 0
}

func uniquify(name string) string {
	for i := 2; i < 1000; i++ {
		candidate := name + "-" + itoa(i)
		if !taken(candidate) {
			return candidate
		}
	}
	// Fallback — timestamp suffix. Extremely unlikely.
	return name + "-" + itoa(int(time.Now().Unix()))
}

func itoa(n int) string {
	// Avoids importing strconv just for one use.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
