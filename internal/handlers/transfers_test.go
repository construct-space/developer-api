package handlers

import (
	"testing"

	"construct/dev-portal/internal/models"
)

func TestOrgOwnerCanRespondToOrgTransfer(t *testing.T) {
	caller := &callerIdentity{
		UserID:                   "user-owner",
		OrgID:                    "org-1",
		CanManageOrgPublisher:    true,
		CanTransferOrgSpaceOwner: true,
	}
	toOrgID := "org-1"
	transfer := &models.SpaceTransfer{ToOrgID: &toOrgID}

	if !canRespondToTransfer(caller, transfer) {
		t.Fatal("expected org owner to respond to incoming org transfer")
	}
}

func TestOrgPublisherManagerCannotRespondToOrgTransferWithoutOwner(t *testing.T) {
	caller := &callerIdentity{
		UserID:                "user-dev",
		OrgID:                 "org-1",
		CanManageOrgPublisher: true,
	}
	toOrgID := "org-1"
	transfer := &models.SpaceTransfer{ToOrgID: &toOrgID}

	if canRespondToTransfer(caller, transfer) {
		t.Fatal("expected non-owner org publisher manager to be denied for incoming org transfer")
	}
}

func TestOnlyOrgOwnerCanInitiateOrgOwnedSpaceTransfer(t *testing.T) {
	orgID := "org-1"
	space := &models.Space{OwnerOrgID: &orgID}

	admin := &callerIdentity{
		UserID:                "user-admin",
		OrgID:                 orgID,
		CanManageOrgPublisher: true,
	}
	if canInitiateTransfer(admin, space) {
		t.Fatal("expected admin/developer without owner authority to be denied for org-owned space transfer")
	}

	owner := &callerIdentity{
		UserID:                   "user-owner",
		OrgID:                    orgID,
		CanManageOrgPublisher:    true,
		CanTransferOrgSpaceOwner: true,
	}
	if !canInitiateTransfer(owner, space) {
		t.Fatal("expected org owner to transfer org-owned space")
	}
}

func TestCanManageOrgPublisherRolesNormalizesRoleNames(t *testing.T) {
	if !canManageOrgPublisherRoles([]string{" Developer "}) {
		t.Fatal("expected Developer role to manage org publisher")
	}
	if canManageOrgPublisherRoles([]string{"member"}) {
		t.Fatal("expected Member role to be denied")
	}
}

func TestCanTransferOrgSpacesRolesIsOwnerOnly(t *testing.T) {
	if !canTransferOrgSpacesRoles([]string{" Owner "}) {
		t.Fatal("expected Owner role to transfer org spaces")
	}
	if canTransferOrgSpacesRoles([]string{"admin", "developer"}) {
		t.Fatal("expected admin/developer roles to be denied for org space transfers")
	}
}
