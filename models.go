package main

import (
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Guestbook represents a collection of messages for a specific website
type Guestbook struct {
	gorm.Model
	WebsiteURL       string
	AdminUserID      uint `gorm:"index"`
	RequiresApproval bool `gorm:"default:false"`

	ChallengeQuestion      string
	ChallengeAnswer        string
	ChallengeHint          string
	ChallengeFailedMessage string

	AllowedOrigins string // Comma-separated list of allowed origins (e.g. "https://xyz.com,https://example.org"). Empty means allow all.

	CustomPageCSS string `gorm:"type:text"`

	Messages []Message
}

// Message represents a guestbook message
type Message struct {
	gorm.Model
	Name            string
	Text            string
	Website         *string
	Approved        bool
	GuestbookID     uint      `gorm:"index"`
	Guestbook       Guestbook `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	ParentMessageID *uint     `gorm:"index"`
	Replies         []Message `gorm:"foreignKey:ParentMessageID"`
}

// AdminUser represents an admin user with access to the admin panel
type AdminUser struct {
	gorm.Model
	Username               string         `gorm:"uniqueIndex"`
	DisplayName            string         `gorm:""`
	PasswordHash           datatypes.JSON `gorm:"type:json"`
	SessionToken           string         `gorm:"index;unique"`
	Email                  string         `gorm:""`
	EmailVerified          bool           `gorm:"default:false"`
	EmailVerificationToken string         `gorm:"index"`
	PasswordResetToken     string         `gorm:"index"`
	PasswordResetExpiry    int64          `gorm:""`
	EmailNotifications     bool           `gorm:""`
	Guestbooks             []Guestbook    `gorm:"foreignKey:AdminUserID"`
}

// ReplyName returns the display name if set, otherwise the username.
func (u *AdminUser) ReplyName() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}
