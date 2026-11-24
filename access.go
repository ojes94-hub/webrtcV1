package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	protectedRoomPrefix = "pr1_"
	protectedNonceBytes = 10
	maxRateLimitEntries = 10000
)

var protectedBase32 = base32.StdEncoding.WithPadding(base32.NoPadding)

type rateLimitEntry struct {
	count    int
	resetAt  time.Time
	lastSeen time.Time
}

type rateLimiter struct {
	mu         sync.Mutex
	limit      int
	window     time.Duration
	maxEntries int
	entries    map[string]*rateLimitEntry
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	if limit < 1 {
		limit = 1
	}
	if window < time.Second {
		window = time.Minute
	}
	return &rateLimiter{
		limit:      limit,
		window:     window,
		maxEntries: maxRateLimitEntries,
		entries:    make(map[string]*rateLimitEntry),
	}
}

func (l *rateLimiter) allow(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for existingKey, entry := range l.entries {
		if now.Sub(entry.lastSeen) > 2*l.window {
			delete(l.entries, existingKey)
		}
	}

	entry := l.entries[key]
	if entry == nil || !now.Before(entry.resetAt) {
		if entry == nil && len(l.entries) >= l.maxEntries {
			l.evictOldest()
		}
		entry = &rateLimitEntry{resetAt: now.Add(l.window)}
		l.entries[key] = entry
	}
	entry.lastSeen = now
	if entry.count >= l.limit {
		return false, entry.resetAt.Sub(now)
	}
	entry.count++
	return true, entry.resetAt.Sub(now)
}

func (l *rateLimiter) evictOldest() {
	var oldestKey string
	var oldestSeen time.Time
	for key, entry := range l.entries {
		if oldestKey == "" || entry.lastSeen.Before(oldestSeen) {
			oldestKey = key
			oldestSeen = entry.lastSeen
		}
	}
	if oldestKey != "" {
		delete(l.entries, oldestKey)
	}
}

func (l *rateLimiter) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

var (
	rateLimitMu            sync.Mutex
	rateLimitConfig        = ""
	conferenceTokenLimiter = newRateLimiter(60, time.Minute)
	roomCreateLimiter      = newRateLimiter(10, time.Minute)
	roomAccessLimiter      = newRateLimiter(10, 15*time.Minute)
	roomModerateLimiter    = newRateLimiter(60, time.Minute)
)

func syncRateLimiters() {
	tokenLimit, tokenWindow := configuredRateLimit("CONFERENCE_TOKEN_RATE_LIMIT", "CONFERENCE_TOKEN_RATE_WINDOW_SECONDS", 60)
	createLimit, createWindow := configuredRateLimit("ROOM_CREATE_RATE_LIMIT", "ROOM_CREATE_RATE_WINDOW_SECONDS", 10)
	moderateLimit, moderateWindow := configuredRateLimit("ROOM_MODERATE_RATE_LIMIT", "ROOM_MODERATE_RATE_WINDOW_SECONDS", 60)
	accessLimit, accessWindow := configuredRateLimitDefaults(
		"ROOM_ACCESS_ATTEMPT_LIMIT", "ROOM_ACCESS_ATTEMPT_WINDOW_SECONDS", 10, 900,
	)
	config := strconv.Itoa(tokenLimit) + "/" + tokenWindow.String() + "/" + strconv.Itoa(createLimit) + "/" + createWindow.String() +
		"/" + strconv.Itoa(moderateLimit) + "/" + moderateWindow.String() +
		"/" + strconv.Itoa(accessLimit) + "/" + accessWindow.String()

	rateLimitMu.Lock()
	defer rateLimitMu.Unlock()
	if config == rateLimitConfig {
		return
	}
	conferenceTokenLimiter = newRateLimiter(tokenLimit, tokenWindow)
	roomCreateLimiter = newRateLimiter(createLimit, createWindow)
	roomModerateLimiter = newRateLimiter(moderateLimit, moderateWindow)
	roomAccessLimiter = newRateLimiter(accessLimit, accessWindow)
	rateLimitConfig = config
}

func configuredRateLimit(limitName, windowName string, defaultLimit int) (int, time.Duration) {
	return configuredRateLimitDefaults(limitName, windowName, defaultLimit, 60)
}

func configuredRateLimitDefaults(limitName, windowName string, defaultLimit, defaultWindowSeconds int) (int, time.Duration) {
	limit := defaultLimit
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(limitName))); err == nil && value > 0 {
		limit = value
	}
	windowSeconds := defaultWindowSeconds
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(windowName))); err == nil && value >= 1 {
		windowSeconds = value
	}
	return limit, time.Duration(windowSeconds) * time.Second
}

func allowRateLimit(w http.ResponseWriter, r *http.Request, limiter *rateLimiter) bool {
	return allowRateLimitKey(w, r, limiter, clientIP(r))
}

func allowRateLimitKey(w http.ResponseWriter, r *http.Request, limiter *rateLimiter, key string) bool {
	allowed, retryAfter := limiter.allow(key, time.Now())
	if allowed {
		return true
	}
	seconds := int(retryAfter / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	http.Error(w, "too many requests", http.StatusTooManyRequests)
	return false
}

func clientIP(r *http.Request) string {
	trustedProxy := strings.EqualFold(strings.TrimSpace(os.Getenv("RENDER")), "true") ||
		strings.TrimSpace(os.Getenv("RENDER_SERVICE_ID")) != "" ||
		strings.TrimSpace(os.Getenv("RENDER_INSTANCE_ID")) != ""
	if trustedProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			first := strings.TrimSpace(strings.Split(forwarded, ",")[0])
			if first != "" {
				return first
			}
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func roomAccessSecret() []byte {
	return []byte(strings.TrimSpace(os.Getenv("ROOM_ACCESS_SECRET")))
}

func isRoomAccessEnabled() bool {
	return len(roomAccessSecret()) > 0
}

func randomProtectedRoom() (roomID, accessCode string, err error) {
	nonce := make([]byte, protectedNonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return "", "", err
	}
	nonceText := strings.ToLower(protectedBase32.EncodeToString(nonce))
	tag := protectedRoomTag(nonceText)
	return protectedRoomPrefix + nonceText + "_" + tag, protectedRoomAccessCode(nonceText), nil
}

func protectedRoomTag(nonceText string) string {
	mac := hmac.New(sha256.New, roomAccessSecret())
	_, _ = mac.Write([]byte("protected-room-id\x00" + nonceText))
	sum := mac.Sum(nil)
	return strings.ToLower(protectedBase32.EncodeToString(sum[:10]))
}

func protectedRoomAccessCode(nonceText string) string {
	mac := hmac.New(sha256.New, roomAccessSecret())
	_, _ = mac.Write([]byte("protected-room-code\x00" + nonceText))
	return strings.ToLower(protectedBase32.EncodeToString(mac.Sum(nil)[:16]))
}

func protectedRoomNonce(roomID string) (string, bool) {
	if _, ok := protectedRoomNonceShape(roomID); !ok {
		return "", false
	}
	remainder := strings.TrimPrefix(roomID, protectedRoomPrefix)
	parts := strings.Split(remainder, "_")
	nonceText, tagText := parts[0], parts[1]
	expectedTag := protectedRoomTag(nonceText)
	if subtle.ConstantTimeCompare([]byte(tagText), []byte(expectedTag)) != 1 {
		return "", false
	}
	return nonceText, true
}

func protectedRoomNonceShape(roomID string) (string, bool) {
	if !strings.HasPrefix(roomID, protectedRoomPrefix) {
		return "", false
	}
	remainder := strings.TrimPrefix(roomID, protectedRoomPrefix)
	parts := strings.Split(remainder, "_")
	if len(parts) != 2 {
		return "", false
	}
	nonceText, tagText := parts[0], parts[1]
	if len(nonceText) != 16 || len(tagText) != 16 {
		return "", false
	}
	for _, char := range nonceText + tagText {
		if !((char >= 'a' && char <= 'z') || (char >= '2' && char <= '7')) {
			return "", false
		}
	}
	return nonceText, true
}

func verifyProtectedRoomAccess(roomID, accessCode string) bool {
	allowed, err := protectedRoomAccess(roomID, accessCode)
	return err == nil && allowed
}

func protectedRoomAccess(roomID, accessCode string) (bool, error) {
	if isProtectedRoomV2ID(roomID) {
		db, err := protectedRoomDB()
		if err != nil {
			return false, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var storedHash []byte
		err = db.QueryRowContext(ctx,
			"SELECT password_hash FROM protected_rooms WHERE room_id = $1", roomID,
		).Scan(&storedHash)
		if errors.Is(err, sql.ErrNoRows) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if !isRoomAccessEnabled() || !validProtectedRoomV2ID(roomID) {
			return false, nil
		}
		expectedHash := protectedRoomPasswordHash(roomID, accessCode)
		return len(storedHash) == len(expectedHash) &&
			subtle.ConstantTimeCompare(storedHash, expectedHash) == 1, nil
	}
	if !isRoomAccessEnabled() {
		return true, nil
	}
	nonceText, shape := protectedRoomNonceShape(roomID)
	if !shape {
		return true, nil
	}
	validNonce, protected := protectedRoomNonce(roomID)
	if !protected || validNonce != nonceText {
		return false, nil
	}
	expected := protectedRoomAccessCode(nonceText)
	if len(accessCode) != len(expected) {
		return false, nil
	}
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(strings.TrimSpace(accessCode))), []byte(expected)) == 1, nil
}

func roomAccessRateLimited(w http.ResponseWriter, r *http.Request, roomID string) bool {
	if !isRoomAccessEnabled() || !isRecognizedProtectedRoomID(roomID) {
		return false
	}
	syncRateLimiters()
	rateLimitMu.Lock()
	limiter := roomAccessLimiter
	rateLimitMu.Unlock()
	return !allowRateLimit(w, r, limiter)
}

func isRecognizedProtectedRoomID(roomID string) bool {
	if validProtectedRoomV2ID(roomID) {
		return true
	}
	_, recognized := protectedRoomNonce(roomID)
	return recognized
}

func handleRoomCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	syncRateLimiters()
	rateLimitMu.Lock()
	limiter := roomCreateLimiter
	rateLimitMu.Unlock()
	if !allowRateLimit(w, r, limiter) {
		return
	}
	if !isRoomAccessEnabled() {
		http.Error(w, "room access control is not configured", http.StatusServiceUnavailable)
		return
	}

	if r.Body != nil {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") &&
			r.ContentLength != 0 {
			http.Error(w, "content type must be application/json", http.StatusBadRequest)
			return
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, 8*1024))
		var input struct{}
		if err := decoder.Decode(&input); err != nil && err != io.EOF {
			http.Error(w, "invalid JSON request", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			http.Error(w, "request must contain one JSON object", http.StatusBadRequest)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	roomID, meetingID, accessCode, hostKey, err := createStoredProtectedRoom(ctx)
	if err != nil {
		http.Error(w, "protected room service is unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Room       string `json:"room"`
		AccessCode string `json:"accessCode"`
		MeetingID  string `json:"meetingId"`
		HostKey    string `json:"hostKey"`
	}{Room: roomID, AccessCode: accessCode, MeetingID: meetingID, HostKey: hostKey})
}

func handleRoomResolve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isRoomAccessEnabled() {
		http.Error(w, "room access control is not configured", http.StatusServiceUnavailable)
		return
	}
	syncRateLimiters()
	rateLimitMu.Lock()
	limiter := roomAccessLimiter
	rateLimitMu.Unlock()
	if !allowRateLimit(w, r, limiter) {
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "content type must be application/json", http.StatusBadRequest)
		return
	}
	var input struct {
		MeetingID string `json:"meetingId"`
		Password  string `json:"password"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8*1024))
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "request must contain one JSON object", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	roomID, allowed, err := resolveStoredMeeting(ctx, input.MeetingID, input.Password)
	if err != nil {
		http.Error(w, "protected room service is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !allowed {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(struct {
		Room       string `json:"room"`
		AccessCode string `json:"accessCode"`
	}{Room: roomID, AccessCode: input.Password})
}
