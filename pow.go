package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"guestbook/constants"

	"github.com/go-chi/chi/v5"
)

// challengeEntry stores metadata for an issued PoW challenge.
type challengeEntry struct {
	guestbookID uint
	createdAt   time.Time
}

// ChallengeStore is a thread-safe in-memory store for PoW challenges.
type ChallengeStore struct {
	mu         sync.Mutex
	challenges map[string]challengeEntry
}

// NewChallengeStore creates a new empty ChallengeStore.
func NewChallengeStore() *ChallengeStore {
	return &ChallengeStore{
		challenges: make(map[string]challengeEntry),
	}
}

// GenerateChallenge creates a new random challenge for the given guestbook,
// stores it, and returns the hex-encoded challenge string.
func (cs *ChallengeStore) GenerateChallenge(guestbookID uint) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	challenge := hex.EncodeToString(buf)

	cs.mu.Lock()
	cs.challenges[challenge] = challengeEntry{
		guestbookID: guestbookID,
		createdAt:   time.Now(),
	}
	cs.mu.Unlock()

	return challenge, nil
}

// VerifyPow validates a challenge+nonce pair:
//   - The challenge must exist and belong to the given guestbook.
//   - The challenge must not be expired (10-minute TTL).
//   - SHA-256(challenge + nonce) must have the required leading zero bits.
//
// On successful verification the challenge is consumed (deleted).
func (cs *ChallengeStore) VerifyPow(challenge, nonce string, guestbookID uint) bool {
	cs.mu.Lock()
	entry, exists := cs.challenges[challenge]
	if !exists {
		cs.mu.Unlock()
		return false
	}
	// Check guestbook ownership
	if entry.guestbookID != guestbookID {
		cs.mu.Unlock()
		return false
	}
	// Check expiry
	ttl := time.Duration(constants.POW_CHALLENGE_TTL_MINUTES) * time.Minute
	if time.Since(entry.createdAt) > ttl {
		delete(cs.challenges, challenge)
		cs.mu.Unlock()
		return false
	}
	// Consume the challenge so it can't be reused
	delete(cs.challenges, challenge)
	cs.mu.Unlock()

	// Verify the proof of work: SHA-256(challenge + nonce) must have
	// POW_DIFFICULTY leading zero bits.
	hash := sha256.Sum256([]byte(challenge + nonce))
	return hasLeadingZeroBits(hash[:], constants.POW_DIFFICULTY)
}

// hasLeadingZeroBits checks whether the byte slice has at least n leading zero bits.
func hasLeadingZeroBits(data []byte, n int) bool {
	fullBytes := n / 8
	remainBits := n % 8

	for i := 0; i < fullBytes; i++ {
		if i >= len(data) || data[i] != 0 {
			return false
		}
	}
	if remainBits > 0 {
		if fullBytes >= len(data) {
			return false
		}
		// The top remainBits of this byte must be zero.
		mask := byte(0xFF << (8 - remainBits))
		if data[fullBytes]&mask != 0 {
			return false
		}
	}
	return true
}

// CleanupExpired removes all challenges older than the TTL. Intended to be run
// periodically in a goroutine.
func (cs *ChallengeStore) CleanupExpired() {
	ttl := time.Duration(constants.POW_CHALLENGE_TTL_MINUTES) * time.Minute
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for k, v := range cs.challenges {
		if time.Since(v.createdAt) > ttl {
			delete(cs.challenges, k)
		}
	}
}

// StartCleanupLoop runs CleanupExpired every 5 minutes in the background.
func (cs *ChallengeStore) StartCleanupLoop() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			cs.CleanupExpired()
		}
	}()
}

// Global challenge store, initialized in main().
var powChallengeStore *ChallengeStore

// PowChallengeHandler handles GET /api/pow-challenge/{guestbookID}.
// It returns a JSON object with a fresh challenge string.
func PowChallengeHandler(w http.ResponseWriter, r *http.Request) {
	guestbookID := chi.URLParam(r, "guestbookID")

	var guestbook Guestbook
	result := db.First(&guestbook, guestbookID)
	if result.Error != nil {
		http.Error(w, "Guestbook not found", http.StatusNotFound)
		return
	}

	if !guestbook.PowEnabled {
		http.Error(w, "Proof of work is not enabled for this guestbook", http.StatusBadRequest)
		return
	}

	challenge, err := powChallengeStore.GenerateChallenge(guestbook.ID)
	if err != nil {
		log.Printf("Error generating PoW challenge: %v", err)
		http.Error(w, "Error generating challenge", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"challenge":  challenge,
		"difficulty": constants.POW_DIFFICULTY,
	})
}
