package models

import "time"

// SpacePublish is an append-only audit row written on every successful
// publish. The Space row only carries the latest state ("who published
// last", "what's the current bundle URL"), which loses prior publishers
// and prior bundles. This table is the long-form history used by:
//   - Oracle: per-version source download for review
//   - My portal: "Published by {name} on {date}" trace for org spaces
//   - Future: bundle rollback, deprecation tooling
//
// Owner snapshot fields capture who owned the space at publish time —
// not just who published it — so the trace survives later transfers
// (transferring ownership doesn't rewrite history).
type SpacePublish struct {
	ID              uint   `gorm:"primaryKey" json:"id"`
	SpaceID         uint   `gorm:"index;not null" json:"spaceId"`
	Version         string `gorm:"size:50;not null;index" json:"version"`
	PublisherUserID string `gorm:"size:100;index;not null" json:"publisherUserId"`
	OwnerUserID     *string `gorm:"size:100" json:"ownerUserId,omitempty"`
	OwnerOrgID      *string `gorm:"size:100" json:"ownerOrgId,omitempty"`
	SourcePath      *string `gorm:"type:text" json:"-"`
	BundleURL       *string `gorm:"type:text" json:"bundleUrl,omitempty"`
	BundlePath      *string `gorm:"type:text" json:"-"`
	BuildSize       int64   `gorm:"default:0" json:"buildSize"`
	BuildDuration   *string `gorm:"size:50" json:"buildDuration,omitempty"`
	BuildChecksum   *string `gorm:"size:200" json:"buildChecksum,omitempty"`
	PublishedAt     time.Time `gorm:"not null;index" json:"publishedAt"`
}

func (SpacePublish) TableName() string { return "space_publishes" }

// HasSource reports whether this publish has a stored source artifact
// (R2 URL or local path). Always-stored today, but historical rows
// from the pre-table era won't have anything to point at — handlers
// gate the source-download endpoint on this.
func (p *SpacePublish) HasSource() bool {
	if p == nil {
		return false
	}
	if p.SourcePath != nil && *p.SourcePath != "" {
		return true
	}
	return false
}
