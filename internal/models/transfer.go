package models

import "time"

// SpaceTransfer is a two-sided handshake for transferring space ownership
// between identities (personal user ↔ org). Replaces the legacy one-shot
// TransferOwnership flow once the handshake endpoints ship.
//
// Lifecycle: pending → accepted | declined | cancelled | expired.
// Expiry is 14 days from CreatedAt.
type SpaceTransfer struct {
	ID      uint `gorm:"primaryKey" json:"id"`
	SpaceID uint `gorm:"index;not null" json:"spaceId"`

	// Source ownership snapshot at request time. Exactly one of FromUserID /
	// FromOrgID is non-nil.
	FromUserID *string `gorm:"column:from_user_id;size:100;index" json:"fromUserId,omitempty"`
	FromOrgID  *string `gorm:"column:from_org_id;size:100;index" json:"fromOrgId,omitempty"`

	// Target. Exactly one of ToUserID / ToOrgID is non-nil.
	ToUserID *string `gorm:"column:to_user_id;size:100;index" json:"toUserId,omitempty"`
	ToOrgID  *string `gorm:"column:to_org_id;size:100;index" json:"toOrgId,omitempty"`

	// User UUID that clicked "Transfer".
	InitiatedBy string `gorm:"size:100;not null" json:"initiatedBy"`

	Status  string  `gorm:"size:20;not null;default:pending;index" json:"status"`
	Message *string `gorm:"type:text" json:"message,omitempty"`

	CreatedAt   time.Time  `json:"createdAt"`
	RespondedAt *time.Time `json:"respondedAt,omitempty"`
	ExpiresAt   time.Time  `json:"expiresAt"`
}

func (SpaceTransfer) TableName() string { return "space_transfers" }

// IsExpired reports whether the transfer window has elapsed.
func (t *SpaceTransfer) IsExpired(now time.Time) bool {
	return now.After(t.ExpiresAt)
}
