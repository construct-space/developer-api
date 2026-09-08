package handlers

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"construct/dev-portal/internal/database"
	"construct/dev-portal/internal/models"
	"construct/dev-portal/internal/services"
)

// Transfer window and re-transfer lock.
const (
	transferExpiryDays   = 14
	retransferLockDays   = 7
	spaceStatusSubmitted = "submitted"
	spaceStatusApproved  = "approved"
	spaceStatusArchived  = "archived"
)

// callerIdentity is the authenticated actor behind a transfer request.
// Either personal (UserID set, OrgID empty) or org (UserID + OrgID +
// CanManageOrgPublisher). Org publisher managers are owners, admins, or
// members with the Developer role. Org space ownership transfer is tighter:
// only the org owner can accept incoming org transfers or transfer org-owned
// spaces onward.
type callerIdentity struct {
	UserID                   string
	OrgID                    string
	CanManageOrgPublisher    bool
	CanTransferOrgSpaceOwner bool
}

// resolveCaller returns the user making the request and, if they're in an
// org, their role. nil if unauthenticated.
func resolveCaller(r *http.Request) *callerIdentity {
	if userID, orgID, canManage, canTransfer, ok := gatewayOrgContext(r); ok {
		c := &callerIdentity{
			UserID:                   userID,
			OrgID:                    orgID,
			CanManageOrgPublisher:    canManage,
			CanTransferOrgSpaceOwner: canTransfer,
		}
		if m, _ := services.GetMembership(Cfg, userID); m != nil {
			c.CanManageOrgPublisher = c.CanManageOrgPublisher || m.CanEnrollOrgAsPublisher()
			c.CanTransferOrgSpaceOwner = c.CanTransferOrgSpaceOwner || m.IsOwner || canTransferOrgSpacesRoles(m.Roles)
		}
		return c
	}

	userID := currentUserID(r)
	if userID == "" {
		return nil
	}
	c := &callerIdentity{UserID: userID}

	// Ask source whether the user is in an org, and if so what role they hold.
	m, _ := services.GetMembership(Cfg, c.UserID)
	if m != nil {
		c.OrgID = m.OrgID
		c.CanManageOrgPublisher = m.CanEnrollOrgAsPublisher()
		c.CanTransferOrgSpaceOwner = m.IsOwner || canTransferOrgSpacesRoles(m.Roles)
	}
	return c
}

// POST /api/spaces/{name}/transfers
// Body: { "to_user_id": "..." } OR { "to_org_id": "..." }, optional "message".
// Initiator must be the current personal owner, or the owner of the owning org.
// Target must be an enrolled publisher.
func CreateTransfer(w http.ResponseWriter, r *http.Request) {
	caller := resolveCaller(r)
	if caller == nil {
		WriteJSON(w, 401, map[string]any{"error": "Authentication required"})
		return
	}

	spaceName := r.PathValue("name")
	var space models.Space
	if err := database.DB.Where("name = ?", spaceName).First(&space).Error; err != nil {
		WriteJSON(w, 404, map[string]any{"error": "Space not found"})
		return
	}

	// Authorization to initiate: personal owner, or owner of owning org.
	if !canInitiateTransfer(caller, &space) {
		WriteJSON(w, 403, map[string]any{"error": "You don't have permission to transfer this space"})
		return
	}

	// Guardrail: block if space is in review.
	if space.Status == spaceStatusSubmitted {
		WriteJSON(w, 409, map[string]any{
			"error": "cannot transfer while under review",
			"code":  "space_in_review",
		})
		return
	}

	// Guardrail: 7-day re-transfer lock.
	if space.UpdatedAt != nil && time.Since(*space.UpdatedAt) < retransferLockDays*24*time.Hour {
		if hasRecentAcceptedTransfer(space.ID) {
			WriteJSON(w, 409, map[string]any{
				"error": "space was recently transferred; try again after the lock period",
				"code":  "retransfer_locked",
			})
			return
		}
	}

	body, err := parseBody(r)
	if err != nil {
		WriteJSON(w, 400, map[string]any{"error": "Invalid JSON"})
		return
	}
	toUserID, _ := body["to_user_id"].(string)
	toOrgID, _ := body["to_org_id"].(string)
	msg, _ := body["message"].(string)

	if (toUserID == "") == (toOrgID == "") {
		WriteJSON(w, 400, map[string]any{"error": "exactly one of to_user_id or to_org_id is required"})
		return
	}

	// Verify target is an enrolled publisher.
	if toUserID != "" {
		var p models.Publisher
		if err := database.DB.Where("user_id = ?", toUserID).First(&p).Error; err != nil {
			WriteJSON(w, 422, map[string]any{"error": "target user is not an enrolled publisher"})
			return
		}
	} else {
		var p models.Publisher
		if err := database.DB.Where("org_id = ?", toOrgID).First(&p).Error; err != nil {
			WriteJSON(w, 422, map[string]any{"error": "target org is not an enrolled publisher"})
			return
		}
	}

	// Cancel any existing pending transfer for this space — one at a time.
	database.DB.Model(&models.SpaceTransfer{}).
		Where("space_id = ? AND status = ?", space.ID, "pending").
		Updates(map[string]any{"status": "cancelled", "responded_at": time.Now()})

	t := models.SpaceTransfer{
		SpaceID:     space.ID,
		FromUserID:  space.OwnerUserID,
		FromOrgID:   space.OwnerOrgID,
		ToUserID:    strPtrOrNil(toUserID),
		ToOrgID:     strPtrOrNil(toOrgID),
		InitiatedBy: caller.UserID,
		Status:      "pending",
		Message:     strPtrOrNil(msg),
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(transferExpiryDays * 24 * time.Hour),
	}
	if err := database.DB.Create(&t).Error; err != nil {
		log.Printf("[transfer] create failed: %v", err)
		WriteJSON(w, 500, map[string]any{"error": "failed to create transfer"})
		return
	}

	WriteJSON(w, 201, map[string]any{"transfer": t})
}

// GET /api/transfers/incoming — pending transfers targeting the caller.
func ListIncomingTransfers(w http.ResponseWriter, r *http.Request) {
	caller := resolveCaller(r)
	if caller == nil {
		WriteJSON(w, 401, map[string]any{"error": "Authentication required"})
		return
	}

	q := database.DB.Model(&models.SpaceTransfer{}).Where("status = ?", "pending")
	if caller.CanTransferOrgSpaceOwner && caller.OrgID != "" {
		q = q.Where("to_user_id = ? OR to_org_id = ?", caller.UserID, caller.OrgID)
	} else {
		q = q.Where("to_user_id = ?", caller.UserID)
	}

	var transfers []models.SpaceTransfer
	q.Order("created_at DESC").Find(&transfers)

	WriteJSON(w, 200, map[string]any{"transfers": withSpaceNames(transfers)})
}

// GET /api/transfers/outgoing — pending transfers initiated by the caller.
func ListOutgoingTransfers(w http.ResponseWriter, r *http.Request) {
	caller := resolveCaller(r)
	if caller == nil {
		WriteJSON(w, 401, map[string]any{"error": "Authentication required"})
		return
	}

	var transfers []models.SpaceTransfer
	database.DB.
		Where("status = ? AND initiated_by = ?", "pending", caller.UserID).
		Order("created_at DESC").
		Find(&transfers)

	WriteJSON(w, 200, map[string]any{"transfers": withSpaceNames(transfers)})
}

// withSpaceNames joins display + slug names onto each transfer so the UI
// renders "Frogger Game Space" instead of "Space #42". One query per batch
// (GORM IN-list) rather than N per row.
func withSpaceNames(ts []models.SpaceTransfer) []map[string]any {
	out := make([]map[string]any, 0, len(ts))
	if len(ts) == 0 {
		return out
	}
	ids := make([]uint, 0, len(ts))
	for _, t := range ts {
		ids = append(ids, t.SpaceID)
	}
	var spaces []models.Space
	database.DB.Select("id, name, display_name").Where("id IN ?", ids).Find(&spaces)
	byID := make(map[uint]models.Space, len(spaces))
	for _, s := range spaces {
		byID[s.ID] = s
	}
	for _, t := range ts {
		row := map[string]any{
			"id":          t.ID,
			"spaceId":     t.SpaceID,
			"fromUserId":  t.FromUserID,
			"fromOrgId":   t.FromOrgID,
			"toUserId":    t.ToUserID,
			"toOrgId":     t.ToOrgID,
			"initiatedBy": t.InitiatedBy,
			"status":      t.Status,
			"message":     t.Message,
			"createdAt":   t.CreatedAt,
			"respondedAt": t.RespondedAt,
			"expiresAt":   t.ExpiresAt,
		}
		if s, ok := byID[t.SpaceID]; ok {
			row["spaceName"] = s.Name
			row["spaceDisplayName"] = s.DisplayName
		}
		out = append(out, row)
	}
	return out
}

// POST /api/transfers/{id}/accept
// Target's authorized actor accepts. Ownership fields on the Space flip
// atomically with the transfer status update.
func AcceptTransfer(w http.ResponseWriter, r *http.Request) {
	caller := resolveCaller(r)
	if caller == nil {
		WriteJSON(w, 401, map[string]any{"error": "Authentication required"})
		return
	}

	t, space, err := loadTransferAndSpace(r)
	if err != nil {
		WriteJSON(w, 404, map[string]any{"error": err.Error()})
		return
	}

	if !canRespondToTransfer(caller, t) {
		WriteJSON(w, 403, map[string]any{"error": "You can't respond to this transfer"})
		return
	}
	if t.Status != "pending" {
		WriteJSON(w, 409, map[string]any{"error": "transfer is not pending", "status": t.Status})
		return
	}
	if t.IsExpired(time.Now()) {
		database.DB.Model(t).Update("status", "expired")
		WriteJSON(w, 409, map[string]any{"error": "transfer has expired"})
		return
	}

	// Re-verify target is still an enrolled publisher at accept time.
	newPublisher, err := lookupTargetPublisher(t)
	if err != nil {
		WriteJSON(w, 422, map[string]any{"error": "target is no longer an enrolled publisher"})
		return
	}

	// Flip ownership atomically.
	tx := database.DB.Begin()
	updates := map[string]any{
		"owner_user_id":     t.ToUserID,
		"owner_org_id":      t.ToOrgID,
		"author":            newPublisher.Name,
		"publisher_name":    &newPublisher.Name,
		"publisher_user_id": newPublisher.UserID,
	}
	if err := tx.Model(space).Updates(updates).Error; err != nil {
		tx.Rollback()
		WriteJSON(w, 500, map[string]any{"error": "failed to update space"})
		return
	}
	now := time.Now()
	if err := tx.Model(t).Updates(map[string]any{"status": "accepted", "responded_at": &now}).Error; err != nil {
		tx.Rollback()
		WriteJSON(w, 500, map[string]any{"error": "failed to update transfer"})
		return
	}
	if err := tx.Commit().Error; err != nil {
		WriteJSON(w, 500, map[string]any{"error": "commit failed"})
		return
	}

	WriteJSON(w, 200, map[string]any{
		"transfer": t,
		"space": map[string]any{
			"name":        space.Name,
			"ownerUserId": t.ToUserID,
			"ownerOrgId":  t.ToOrgID,
			"publisher":   newPublisher.Name,
		},
	})
}

// POST /api/transfers/{id}/decline
func DeclineTransfer(w http.ResponseWriter, r *http.Request) {
	caller := resolveCaller(r)
	if caller == nil {
		WriteJSON(w, 401, map[string]any{"error": "Authentication required"})
		return
	}
	t, _, err := loadTransferAndSpace(r)
	if err != nil {
		WriteJSON(w, 404, map[string]any{"error": err.Error()})
		return
	}
	if !canRespondToTransfer(caller, t) {
		WriteJSON(w, 403, map[string]any{"error": "You can't respond to this transfer"})
		return
	}
	if t.Status != "pending" {
		WriteJSON(w, 409, map[string]any{"error": "transfer is not pending", "status": t.Status})
		return
	}

	now := time.Now()
	database.DB.Model(t).Updates(map[string]any{"status": "declined", "responded_at": &now})
	WriteJSON(w, 200, map[string]any{"transfer": t})
}

// DELETE /api/transfers/{id} — initiator cancels.
func CancelTransfer(w http.ResponseWriter, r *http.Request) {
	caller := resolveCaller(r)
	if caller == nil {
		WriteJSON(w, 401, map[string]any{"error": "Authentication required"})
		return
	}
	t, _, err := loadTransferAndSpace(r)
	if err != nil {
		WriteJSON(w, 404, map[string]any{"error": err.Error()})
		return
	}
	if t.InitiatedBy != caller.UserID {
		WriteJSON(w, 403, map[string]any{"error": "only the initiator can cancel"})
		return
	}
	if t.Status != "pending" {
		WriteJSON(w, 409, map[string]any{"error": "transfer is not pending", "status": t.Status})
		return
	}

	now := time.Now()
	database.DB.Model(t).Updates(map[string]any{"status": "cancelled", "responded_at": &now})
	WriteJSON(w, 200, map[string]any{"transfer": t})
}

// --- helpers ---

func canInitiateTransfer(c *callerIdentity, s *models.Space) bool {
	if s.OwnerUserID != nil && *s.OwnerUserID == c.UserID {
		return true
	}
	if s.OwnerOrgID != nil && c.CanTransferOrgSpaceOwner && c.OrgID == *s.OwnerOrgID {
		return true
	}
	return false
}

func canRespondToTransfer(c *callerIdentity, t *models.SpaceTransfer) bool {
	if t.ToUserID != nil && *t.ToUserID == c.UserID {
		return true
	}
	if t.ToOrgID != nil && c.CanTransferOrgSpaceOwner && c.OrgID == *t.ToOrgID {
		return true
	}
	return false
}

func loadTransferAndSpace(r *http.Request) (*models.SpaceTransfer, *models.Space, error) {
	idStr := r.PathValue("id")
	id64, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return nil, nil, errStr("invalid transfer id")
	}
	var t models.SpaceTransfer
	if err := database.DB.First(&t, uint(id64)).Error; err != nil {
		return nil, nil, errStr("transfer not found")
	}
	var space models.Space
	if err := database.DB.First(&space, t.SpaceID).Error; err != nil {
		return &t, nil, errStr("space not found")
	}
	return &t, &space, nil
}

func lookupTargetPublisher(t *models.SpaceTransfer) (*models.Publisher, error) {
	var p models.Publisher
	var err error
	if t.ToUserID != nil {
		err = database.DB.Where("user_id = ?", *t.ToUserID).First(&p).Error
	} else if t.ToOrgID != nil {
		err = database.DB.Where("org_id = ?", *t.ToOrgID).First(&p).Error
	} else {
		return nil, errStr("transfer has no target")
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func hasRecentAcceptedTransfer(spaceID uint) bool {
	var count int64
	cutoff := time.Now().Add(-retransferLockDays * 24 * time.Hour)
	database.DB.Model(&models.SpaceTransfer{}).
		Where("space_id = ? AND status = ? AND responded_at > ?", spaceID, "accepted", cutoff).
		Count(&count)
	return count > 0
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

type strError string

func (e strError) Error() string { return string(e) }
func errStr(s string) error      { return strError(s) }
