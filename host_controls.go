package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	maxModerateBodyBytes = 8 * 1024
	maxLiveKitBodyBytes  = 2 * 1024 * 1024
)

type roomModerateRequest struct {
	Room     string          `json:"room"`
	HostKey  json.RawMessage `json:"hostKey"`
	Action   string          `json:"action"`
	Identity json.RawMessage `json:"identity"`
}

type liveKitAdminClaims struct {
	Issuer    string            `json:"iss"`
	Subject   string            `json:"sub"`
	IssuedAt  int64             `json:"iat"`
	NotBefore int64             `json:"nbf"`
	ExpiresAt int64             `json:"exp"`
	TokenID   string            `json:"jti"`
	Video     liveKitAdminGrant `json:"video"`
}

type liveKitAdminGrant struct {
	Room       string `json:"room"`
	RoomAdmin  bool   `json:"roomAdmin,omitempty"`
	RoomCreate bool   `json:"roomCreate,omitempty"`
}

type liveKitTrack struct {
	SID    string          `json:"sid"`
	Source json.RawMessage `json:"source"`
}

type liveKitParticipant struct {
	Identity string         `json:"identity"`
	Metadata string         `json:"metadata"`
	Tracks   []liveKitTrack `json:"tracks"`
}

type liveKitParticipantsResponse struct {
	Participants []liveKitParticipant `json:"participants"`
}

type liveKitRPCError struct {
	StatusCode int
	Code       string
}

func (e *liveKitRPCError) Error() string {
	return fmt.Sprintf("LiveKit request failed with status %d", e.StatusCode)
}

func handleRoomModerate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	syncRateLimiters()
	rateLimitMu.Lock()
	limiter := roomModerateLimiter
	rateLimitMu.Unlock()
	if !allowRateLimit(w, r, limiter) {
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "content type must be application/json", http.StatusBadRequest)
		return
	}

	var input roomModerateRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxModerateBodyBytes))
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "request must contain one JSON object", http.StatusBadRequest)
		return
	}
	if !validRoomID(input.Room) {
		http.Error(w, "invalid room", http.StatusBadRequest)
		return
	}
	identity := ""
	switch input.Action {
	case "mute", "remove":
		var suppliedIdentity string
		if len(input.Identity) == 0 || json.Unmarshal(input.Identity, &suppliedIdentity) != nil {
			http.Error(w, "invalid identity", http.StatusBadRequest)
			return
		}
		cleanedIdentity, err := cleanConferenceUserID(suppliedIdentity)
		if err != nil || cleanedIdentity == "" {
			http.Error(w, "invalid identity", http.StatusBadRequest)
			return
		}
		identity = cleanedIdentity
	case "mute_all", "end":
	default:
		http.Error(w, "invalid action", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	hostKey := ""
	if len(input.HostKey) > 0 {
		_ = json.Unmarshal(input.HostKey, &hostKey)
	}
	var affected int
	var err error
	if input.Action == "end" {
		affected, err = endHostedRoom(ctx, input.Room, hostKey)
	} else {
		affected, err = moderateHostedRoom(ctx, input, hostKey, identity)
	}
	if err != nil {
		writeModerationError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(struct {
		OK       bool `json:"ok"`
		Affected int  `json:"affected"`
	}{OK: true, Affected: affected})
}

func moderateHostedRoom(ctx context.Context, input roomModerateRequest, hostKey, identity string) (int, error) {
	if !validProtectedRoomV2ID(input.Room) {
		return 0, errAccessDenied
	}
	state, err := protectedRoomHostStateContext(ctx, input.Room)
	if err != nil || !verifyStoredRoomHostKey(state, input.Room, hostKey) {
		return 0, errAccessDenied
	}
	if state.EndedAt.Valid {
		return 0, errMeetingEnded
	}
	config, ok := liveKitServiceConfig()
	if !ok {
		return 0, errLiveKitNotConfigured
	}
	adminToken, err := createLiveKitAdminToken(config.apiKey, config.apiSecret, input.Room, time.Now())
	if err != nil {
		return 0, errLiveKitUnavailable
	}

	switch input.Action {
	case "mute":
		participant, err := findLiveKitParticipant(ctx, config, adminToken, input.Room, identity)
		if err != nil {
			return 0, err
		}
		if isLiveKitHost(participant) {
			return 0, errAccessDenied
		}
		return muteMicrophoneTracks(ctx, config, adminToken, input.Room, participant)
	case "mute_all":
		participants, err := listLiveKitParticipants(ctx, config, adminToken, input.Room)
		if err != nil {
			return 0, err
		}
		affected := 0
		for _, participant := range participants {
			if isLiveKitHost(participant) {
				continue
			}
			muted, err := muteMicrophoneTracks(ctx, config, adminToken, input.Room, participant)
			if err != nil {
				return 0, err
			}
			affected += muted
		}
		return affected, nil
	case "remove":
		participant, err := findLiveKitParticipant(ctx, config, adminToken, input.Room, identity)
		if err != nil {
			return 0, err
		}
		if isLiveKitHost(participant) {
			return 0, errAccessDenied
		}
		var response json.RawMessage
		err = liveKitCall(ctx, config, adminToken, "RemoveParticipant", map[string]string{
			"room": input.Room, "identity": identity,
		}, &response)
		if err != nil {
			return 0, participantRPCError(err)
		}
		return 1, nil
	default:
		return 0, errors.New("invalid moderation action")
	}
}

func endHostedRoom(ctx context.Context, roomID, hostKey string) (int, error) {
	if !validProtectedRoomV2ID(roomID) {
		return 0, errAccessDenied
	}
	db, err := protectedRoomDB()
	if err != nil {
		return 0, errAccessDenied
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, errAccessDenied
	}
	defer tx.Rollback()

	state, err := readStoredRoomHostState(ctx, tx, roomID, true)
	if err != nil || !verifyStoredRoomHostKey(state, roomID, hostKey) {
		return 0, errAccessDenied
	}
	if state.EndedAt.Valid {
		return 0, errMeetingEnded
	}
	config, ok := liveKitServiceConfig()
	if !ok {
		return 0, errLiveKitNotConfigured
	}
	adminToken, err := createLiveKitAdminToken(config.apiKey, config.apiSecret, roomID, time.Now())
	if err != nil {
		return 0, errLiveKitUnavailable
	}
	result, err := tx.ExecContext(ctx,
		"UPDATE protected_rooms SET ended_at = now() WHERE room_id = $1 AND host_key_hash IS NOT NULL AND ended_at IS NULL",
		roomID,
	)
	if err != nil {
		return 0, errProtectedRoomUnavailable
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return 0, errProtectedRoomUnavailable
	}
	if updated != 1 {
		return 0, errMeetingEnded
	}
	var response json.RawMessage
	if err := liveKitCall(ctx, config, adminToken, "DeleteRoom", map[string]string{"room": roomID}, &response); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, errProtectedRoomUnavailable
	}
	return 1, nil
}

func muteMicrophoneTracks(ctx context.Context, config liveKitService, token, roomID string, participant liveKitParticipant) (int, error) {
	muted := 0
	for _, track := range participant.Tracks {
		if track.SID == "" || !isMicrophoneSource(track.Source) {
			continue
		}
		var response json.RawMessage
		err := liveKitCall(ctx, config, token, "MutePublishedTrack", map[string]any{
			"room": roomID, "identity": participant.Identity, "trackSid": track.SID, "muted": true,
		}, &response)
		if err != nil {
			return 0, participantRPCError(err)
		}
		muted++
	}
	return muted, nil
}

func findLiveKitParticipant(ctx context.Context, config liveKitService, token, roomID, identity string) (liveKitParticipant, error) {
	participants, err := listLiveKitParticipants(ctx, config, token, roomID)
	if err != nil {
		return liveKitParticipant{}, participantRPCError(err)
	}
	for _, participant := range participants {
		if participant.Identity == identity {
			return participant, nil
		}
	}
	return liveKitParticipant{}, errParticipantNotFound
}

func listLiveKitParticipants(ctx context.Context, config liveKitService, token, roomID string) ([]liveKitParticipant, error) {
	var response liveKitParticipantsResponse
	if err := liveKitCall(ctx, config, token, "ListParticipants", map[string]string{"room": roomID}, &response); err != nil {
		return nil, err
	}
	return response.Participants, nil
}

func isLiveKitHost(participant liveKitParticipant) bool {
	var metadata struct {
		Role string `json:"role"`
	}
	return json.Unmarshal([]byte(participant.Metadata), &metadata) == nil && metadata.Role == "host"
}

func isMicrophoneSource(source json.RawMessage) bool {
	var name string
	if json.Unmarshal(source, &name) == nil {
		return strings.EqualFold(name, "MICROPHONE")
	}
	var number int
	return json.Unmarshal(source, &number) == nil && number == 2
}

type liveKitService struct {
	baseURL   string
	apiKey    string
	apiSecret string
}

func liveKitServiceConfig() (liveKitService, bool) {
	apiKey := strings.TrimSpace(os.Getenv("LIVEKIT_API_KEY"))
	apiSecret := strings.TrimSpace(os.Getenv("LIVEKIT_API_SECRET"))
	endpoint := strings.TrimSpace(os.Getenv("LIVEKIT_URL"))
	if apiKey == "" || apiSecret == "" || endpoint == "" {
		return liveKitService{}, false
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return liveKitService{}, false
	}
	switch parsed.Scheme {
	case "wss":
		parsed.Scheme = "https"
	case "ws":
		parsed.Scheme = "http"
	case "https", "http":
	default:
		return liveKitService{}, false
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return liveKitService{baseURL: strings.TrimRight(parsed.String(), "/"), apiKey: apiKey, apiSecret: apiSecret}, true
}

func createLiveKitAdminToken(apiKey, apiSecret, roomID string, now time.Time) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims := liveKitAdminClaims{
		Issuer:    apiKey,
		Subject:   "server",
		IssuedAt:  now.Unix(),
		NotBefore: now.Unix(),
		ExpiresAt: now.Add(60 * time.Second).Unix(),
		TokenID:   "server-" + fmt.Sprint(now.UnixNano()),
		Video: liveKitAdminGrant{
			Room:       roomID,
			RoomAdmin:  true,
			RoomCreate: true,
		},
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

func liveKitCall(ctx context.Context, config liveKitService, token, method string, requestBody any, responseBody any) error {
	body, err := json.Marshal(requestBody)
	if err != nil {
		return errLiveKitUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		config.baseURL+"/twirp/livekit.RoomService/"+method, strings.NewReader(string(body)))
	if err != nil {
		return errLiveKitUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return errLiveKitUnavailable
	}
	defer response.Body.Close()
	responseData, err := io.ReadAll(io.LimitReader(response.Body, maxLiveKitBodyBytes+1))
	if err != nil || len(responseData) > maxLiveKitBodyBytes {
		return errLiveKitUnavailable
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var rpcError struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(responseData, &rpcError)
		return &liveKitRPCError{StatusCode: response.StatusCode, Code: rpcError.Code}
	}
	if responseBody != nil && len(responseData) > 0 {
		if err := json.Unmarshal(responseData, responseBody); err != nil {
			return errLiveKitUnavailable
		}
	}
	return nil
}

func participantRPCError(err error) error {
	var rpcError *liveKitRPCError
	if errors.As(err, &rpcError) && (rpcError.Code == "not_found" || rpcError.StatusCode == http.StatusNotFound) {
		return errParticipantNotFound
	}
	return err
}

var (
	errAccessDenied             = errors.New("access denied")
	errMeetingEnded             = errors.New("meeting ended")
	errLiveKitNotConfigured     = errors.New("LiveKit is not configured")
	errLiveKitUnavailable       = errors.New("LiveKit is unavailable")
	errParticipantNotFound      = errors.New("participant not found")
	errProtectedRoomUnavailable = errors.New("protected room service is unavailable")
)

func writeModerationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errAccessDenied):
		http.Error(w, "access denied", http.StatusForbidden)
	case errors.Is(err, errMeetingEnded):
		http.Error(w, "meeting ended", http.StatusGone)
	case errors.Is(err, errLiveKitNotConfigured):
		http.Error(w, "conference service is not configured", http.StatusServiceUnavailable)
	case errors.Is(err, errParticipantNotFound):
		http.Error(w, "participant not found", http.StatusNotFound)
	case errors.Is(err, errProtectedRoomUnavailable):
		http.Error(w, "protected room service is unavailable", http.StatusServiceUnavailable)
	default:
		http.Error(w, "LiveKit unavailable", http.StatusBadGateway)
	}
}
