package models

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

type Session struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	Token        string     `gorm:"size:128;uniqueIndex;not null" json:"-"`
	AccessToken  *string    `gorm:"type:text" json:"-"`
	UserID       *string    `gorm:"size:100" json:"user_id,omitempty"`
	Email        *string    `gorm:"size:200" json:"email,omitempty"`
	Name         *string    `gorm:"size:200" json:"name,omitempty"`
	AvatarURL    *string    `gorm:"type:text" json:"avatar_url,omitempty"`
	UserAgent    *string    `gorm:"type:text" json:"user_agent,omitempty"`
	IPAddress    *string    `gorm:"size:255" json:"ip_address,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	CreatedAt    *time.Time `json:"created_at,omitempty"`
	UpdatedAt    *time.Time `json:"-"`
}

func (Session) TableName() string { return "sessions" }

func (s *Session) IsExpired() bool {
	if s.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*s.ExpiresAt)
}

func GenerateSessionToken() string {
	b := make([]byte, 48)
	rand.Read(b)
	return hex.EncodeToString(b)
}
