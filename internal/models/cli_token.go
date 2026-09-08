package models

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

type CLIToken struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	Token     string     `gorm:"size:128;uniqueIndex;not null" json:"-"`
	UserID    string     `gorm:"size:100;not null" json:"user_id"`
	Name      string     `gorm:"size:200" json:"name"`
	Email     string     `gorm:"size:200" json:"email"`
	AvatarURL *string    `gorm:"type:text" json:"avatar_url,omitempty"`
	Label     string     `gorm:"size:200" json:"label"` // e.g. "MacBook Pro"
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`

	// Ephemeral, never persisted. Set only by resolvePublisherKey when the
	// caller authenticated with an org publisher's API key — publish then
	// attributes the resulting Space to the org rather than the user.
	OrgID string `gorm:"-" json:"-"`
}

func (CLIToken) TableName() string { return "cli_tokens" }

func (t *CLIToken) IsExpired() bool {
	if t.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*t.ExpiresAt)
}

func GenerateCLIToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return "cst_live_" + hex.EncodeToString(b)
}
