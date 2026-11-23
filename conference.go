package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

type conferenceTokenRequest struct {
	Room       string `json:"room"`
	Username   string `json:"username"`
	UserID     string `json:"userId,omitempty"`
	AccessCode string `json:"accessCode,omitempty"`
}

type conferenceTokenClaims struct {
	Issuer    string               `json:"iss"`
	Subject   string               `json:"sub"`
	Name      string               `json:"name"`
	IssuedAt  int64                `json:"iat"`
	NotBefore int64                `json:"nbf"`
	ExpiresAt int64                `json:"exp"`
	TokenID   string               `json:"jti"`
	Video     conferenceVideoGrant `json:"video"`
}

type conferenceVideoGrant struct {
	RoomJoin       bool   `json:"roomJoin"`
	Room           string `json:"room"`
	CanPublish     bool   `json:"canPublish"`
	CanSubscribe   bool   `json:"canSubscribe"`
	CanPublishData bool   `json:"canPublishData"`
}

func handleConferenceToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	syncRateLimiters()
	rateLimitMu.Lock()
	limiter := conferenceTokenLimiter
	rateLimitMu.Unlock()
	if !allowRateLimit(w, r, limiter) {
		return
	}

	input, err := parseConferenceTokenRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !validRoomID(input.Room) {
		http.Error(w, "invalid room", http.StatusBadRequest)
		return
	}
	username, err := cleanDisplayName(input.Username)
	if err != nil {
		http.Error(w, "invalid username", http.StatusBadRequest)
		return
	}
	userID, err := cleanConferenceUserID(input.UserID)
	if err != nil {
		http.Error(w, "invalid userId", http.StatusBadRequest)
		return
	}
	if !verifyProtectedRoomAccess(input.Room, input.AccessCode) {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}

	apiKey := strings.TrimSpace(os.Getenv("LIVEKIT_API_KEY"))
	apiSecret := strings.TrimSpace(os.Getenv("LIVEKIT_API_SECRET"))
	livekitURL := strings.TrimSpace(os.Getenv("LIVEKIT_URL"))
	if apiKey == "" || apiSecret == "" || livekitURL == "" {
		http.Error(w, "conference service is not configured", http.StatusServiceUnavailable)
		return
	}

	token, err := createConferenceToken(apiKey, apiSecret, input.Room, username, userID, time.Now())
	if err != nil {
		http.Error(w, "could not create conference token", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token, "url": livekitURL})
}

func parseConferenceTokenRequest(r *http.Request) (conferenceTokenRequest, error) {
	if r.Method == http.MethodGet {
		return conferenceTokenRequest{
			Room:       r.URL.Query().Get("room"),
			Username:   r.URL.Query().Get("username"),
			UserID:     r.URL.Query().Get("userId"),
			AccessCode: r.URL.Query().Get("accessCode"),
		}, nil
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return conferenceTokenRequest{}, errors.New("content type must be application/json")
	}
	var input conferenceTokenRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 8*1024))
	if err := decoder.Decode(&input); err != nil {
		return conferenceTokenRequest{}, errors.New("invalid JSON request")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return conferenceTokenRequest{}, errors.New("request must contain one JSON object")
	}
	return input, nil
}

func cleanConferenceUserID(userID string) (string, error) {
	userID = strings.TrimSpace(userID)
	if utf8.RuneCountInString(userID) > 128 {
		return "", errors.New("userId exceeds 128 characters")
	}
	for _, char := range userID {
		if unicode.IsControl(char) {
			return "", errors.New("userId contains unsupported characters")
		}
	}
	return userID, nil
}

func createConferenceToken(apiKey, apiSecret, room, username, userID string, now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims := conferenceTokenClaims{
		Issuer:    apiKey,
		Subject:   userID,
		Name:      username,
		IssuedAt:  now.Unix(),
		NotBefore: now.Unix(),
		ExpiresAt: now.Add(time.Hour).Unix(),
		TokenID:   uuid.NewString(),
		Video: conferenceVideoGrant{
			RoomJoin:       true,
			Room:           room,
			CanPublish:     true,
			CanSubscribe:   true,
			CanPublishData: true,
		},
	}
	if claims.Subject == "" {
		claims.Subject = uuid.NewString()
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(apiSecret))
	if _, err := mac.Write([]byte(encoded)); err != nil {
		return "", err
	}
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
