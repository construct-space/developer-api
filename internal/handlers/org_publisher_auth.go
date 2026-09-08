package handlers

import (
	"net/http"
	"strings"
)

func canManageOrgPublisherRoles(roles []string) bool {
	for _, role := range roles {
		switch strings.ToLower(strings.TrimSpace(role)) {
		case "owner", "admin", "developer":
			return true
		}
	}
	return false
}

func canTransferOrgSpacesRoles(roles []string) bool {
	for _, role := range roles {
		if strings.ToLower(strings.TrimSpace(role)) == "owner" {
			return true
		}
	}
	return false
}

func gatewayOrgContext(r *http.Request) (userID string, orgID string, canManage bool, canTransfer bool, ok bool) {
	id := gatewayIdentity(r)
	if id == nil || id.Scope != "org" || id.OrgID == "" {
		return "", "", false, false, false
	}
	return id.UserID, id.OrgID, canManageOrgPublisherRoles(id.Roles), canTransferOrgSpacesRoles(id.Roles), true
}
