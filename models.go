package main

import (
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Guestbook represents a collection of messages for a specific website
type Guestbook struct {
	gorm.Model
	WebsiteURL  string
	AdminUserID uint `gorm:"index"`
	Messages    []Message
}

// Message represents a guestbook message
type Message struct {
	gorm.Model
	Name        string
	Text        string
	Website     *string
	GuestbookID uint      `gorm:"index"`
	Guestbook   Guestbook `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`
}

// AdminUser represents an admin user with access to the admin panel
type AdminUser struct {
	gorm.Model
	Username     string         `gorm:"uniqueIndex"`
	PasswordHash datatypes.JSON `gorm:"type:json"`
	Token        string         `gorm:"index;unique"`
	Guestbooks   []Guestbook    `gorm:"foreignKey:AdminUserID"`
}
