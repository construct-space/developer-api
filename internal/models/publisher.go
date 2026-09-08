package models

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

type Publisher struct {
	ID        uint       `gorm:"primaryKey" json:"-"`
	Name      string     `gorm:"size:100;uniqueIndex;not null" json:"name"`
	// Meaningful for personal publishers (the owner's email). For org
	// publishers the identity is OrgID, so email is blank. No uniqueness
	// constraint — a user's personal email can also appear on their org's
	// publisher row without colliding.
	Email     string     `gorm:"size:200" json:"email,omitempty"`
	APIKey    string     `gorm:"column:api_key;size:64;uniqueIndex;not null" json:"-"`
	// Identity anchor — exactly one of UserID (personal) or OrgID (org) is non-nil.
	UserID    *string    `gorm:"column:user_id;size:100;index" json:"userId,omitempty"`
	OrgID     *string    `gorm:"column:org_id;size:100;index" json:"orgId,omitempty"`
	Website   *string    `gorm:"type:text" json:"website,omitempty"`
	AvatarURL *string    `gorm:"type:text" json:"avatarUrl,omitempty"`
	Verified  bool       `gorm:"default:false" json:"verified"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	UpdatedAt *time.Time `json:"-"`
}

// Kind returns "user", "org", or "legacy" (neither FK set — pre-migration row).
func (p *Publisher) Kind() string {
	switch {
	case p.UserID != nil && *p.UserID != "":
		return "user"
	case p.OrgID != nil && *p.OrgID != "":
		return "org"
	default:
		return "legacy"
	}
}

func (Publisher) TableName() string { return "publishers" }

func (p *Publisher) ToPublicJSON() map[string]any {
	result := map[string]any{
		"name":     p.Name,
		"verified": p.Verified,
	}
	if p.Website != nil {
		result["website"] = *p.Website
	}
	if p.AvatarURL != nil {
		result["avatarUrl"] = *p.AvatarURL
	}
	return result
}

func GenerateAPIKey() string {
	b := make([]byte, 20)
	rand.Read(b)
	return "csk_live_" + hex.EncodeToString(b)
}
