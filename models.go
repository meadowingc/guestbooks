package main

import (
	"net/url"
	"strings"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Guestbook represents a collection of messages for a specific website
type Guestbook struct {
	gorm.Model
	WebsiteURL       string
	AdminUserID      uint `gorm:"index"`
	RequiresApproval bool `gorm:"default:false"`
	PowEnabled       bool `gorm:"default:false"`

	SubmissionAction      SubmissionAction `gorm:"default:''" json:"-"`
	SubmissionMessage     string           `json:"-"`
	SubmissionRedirectURL string           `json:"-"`
	CollectEmail          bool             `gorm:"default:false" json:"-"`
	EmailFieldLabel       string           `json:"-"`
	EmailFieldHelp        string           `json:"-"`

	ChallengeQuestion      string
	ChallengeAnswer        string
	ChallengeHint          string
	ChallengeFailedMessage string

	CustomPageCSS string `gorm:"type:text"`

	Messages []Message
}

// Message represents a guestbook message
type Message struct {
	gorm.Model
	Name            string
	Text            string
	Website         *string
	Email           *string `json:"-"`
	Approved        bool
	GuestbookID     uint      `gorm:"index"`
	Guestbook       Guestbook `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
	ParentMessageID *uint     `gorm:"index"`
	Replies         []Message `gorm:"foreignKey:ParentMessageID"`
}

func (m Message) EmailLink() string {
	if m.Email == nil {
		return ""
	}
	return "mailto:" + strings.ReplaceAll(url.QueryEscape(*m.Email), "+", "%20")
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
