package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestHandleConferenceTokenReturnsSignedJoinToken(t *testing.T) {
	t.Setenv("LIVEKIT_URL", "wss://livekit.example.test")
	t.Setenv("LIVEKIT_API_KEY", "test-api-key")
	t.Setenv("LIVEKIT_API_SECRET", "test-api-secret")
	req := httptest.NewRequest(http.MethodPost, "/api/conference-token", strings.NewReader(`{"room":"team-room","username":"Alice","userId":"native-user-42"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	handleConferenceToken(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", res.Code, http.StatusOK, res.Body.String())
	}
	if res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", res.Header().Get("Cache-Control"))
	}
	var response struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.URL != "wss://livekit.example.test" {
		t.Fatalf("url = %q", response.URL)
	}
	parts := strings.Split(response.Token, ".")
	if len(parts) != 3 {
		t.Fatalf("token segments = %d, want 3", len(parts))
	}
	mac := hmac.New(sha256.New, []byte("test-api-secret"))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		t.Fatal("token signature is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims conferenceTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != "test-api-key" || claims.Subject != "native-user-42" || claims.Name != "Alice" ||
		!claims.Video.RoomJoin || claims.Video.Room != "team-room" ||
		!claims.Video.CanPublish || !claims.Video.CanSubscribe || !claims.Video.CanPublishData {
		t.Fatalf("unexpected token claims: %+v", claims)
	}
}

func TestAPIEndpointsSetCORSHeadersAndHandlePreflight(t *testing.T) {
	handler, err := newHandler()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIVEKIT_URL", "wss://livekit.example.test")
	t.Setenv("LIVEKIT_API_KEY", "test-api-key")
	t.Setenv("LIVEKIT_API_SECRET", "test-api-secret")

	req := httptest.NewRequest(http.MethodOptions, "/api/conference-token", nil)
	req.Header.Set("Origin", "https://native-client.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", res.Code, http.StatusNoContent)
	}
	assertCORSHeaders(t, res)

	req = httptest.NewRequest(http.MethodPost, "/api/conference-token", strings.NewReader(`{"room":"team-room","username":"Alice"}`))
	req.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("conference token status = %d, want %d", res.Code, http.StatusOK)
	}
	assertCORSHeaders(t, res)

	req = httptest.NewRequest(http.MethodGet, "/api/ice-servers", nil)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("ICE server status = %d, want %d", res.Code, http.StatusOK)
	}
	assertCORSHeaders(t, res)

	req = httptest.NewRequest(http.MethodGet, "/api/unregistered", nil)
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("unknown API status = %d, want %d", res.Code, http.StatusNotFound)
	}
	assertCORSHeaders(t, res)
}

func assertCORSHeaders(t *testing.T, res *httptest.ResponseRecorder) {
	t.Helper()
	want := map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Headers": "Content-Type, Authorization",
		"Access-Control-Allow-Methods": "GET, POST, OPTIONS",
	}
	for header, value := range want {
		if got := res.Header().Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
}

func TestHandleConferenceTokenRejectsInvalidRequestsAndMissingConfig(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		configured  bool
		wantStatus  int
	}{
		{name: "invalid room", body: `{"room":"bad room","username":"Alice"}`, contentType: "application/json", configured: true, wantStatus: http.StatusBadRequest},
		{name: "invalid JSON", body: `{`, contentType: "application/json", configured: true, wantStatus: http.StatusBadRequest},
		{name: "missing config", body: `{"room":"team-room","username":"Alice"}`, contentType: "application/json", wantStatus: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("LIVEKIT_URL", "")
			t.Setenv("LIVEKIT_API_KEY", "")
			t.Setenv("LIVEKIT_API_SECRET", "")
			if test.configured {
				t.Setenv("LIVEKIT_URL", "wss://livekit.example.test")
				t.Setenv("LIVEKIT_API_KEY", "test-api-key")
				t.Setenv("LIVEKIT_API_SECRET", "test-api-secret")
			}
			req := httptest.NewRequest(http.MethodPost, "/api/conference-token", strings.NewReader(test.body))
			req.Header.Set("Content-Type", test.contentType)
			res := httptest.NewRecorder()
			handleConferenceToken(res, req)
			if res.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", res.Code, test.wantStatus)
			}
		})
	}
}

func TestServeIndex(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	res := httptest.NewRecorder()

	serveIndex(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("serveIndex status = %d, want %d", res.Code, http.StatusOK)
	}
	if res.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", res.Header().Get("Cache-Control"))
	}
}

func TestRoomRoutesServeAppAndRejectInvalidRoomIDs(t *testing.T) {
	handler, err := newHandler()
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/", "/room/test-room"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want %d", path, res.Code, http.StatusOK)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/room/not%2Fvalid", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("invalid room path status = %d, want %d", res.Code, http.StatusNotFound)
	}
}

func TestServeHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()

	serveHealth(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("serveHealth status = %d, want %d", res.Code, http.StatusOK)
	}
	if res.Body.String() != "ok" {
		t.Fatalf("serveHealth body = %q, want ok", res.Body.String())
	}
}

func TestRoomManagerKeepsPeersIsolated(t *testing.T) {
	manager := NewRoomManager()
	alice := testPeer("alice", "room-one", "Alice")
	bob := testPeer("bob", "room-one", "Bob")
	charlie := testPeer("charlie", "room-two", "Charlie")

	if err := manager.join(alice); err != nil {
		t.Fatal(err)
	}
	if err := manager.join(bob); err != nil {
		t.Fatal(err)
	}
	if err := manager.join(charlie); err != nil {
		t.Fatal(err)
	}

	if got := (<-alice.out).Type; got != "welcome" {
		t.Fatalf("first event to Alice = %q, want welcome", got)
	}
	if got := (<-alice.out).Type; got != "new-peer" {
		t.Fatalf("second event to Alice = %q, want new-peer", got)
	}
	if got := (<-bob.out).Type; got != "welcome" {
		t.Fatalf("first event to Bob = %q, want welcome", got)
	}
	if got := (<-charlie.out).Type; got != "welcome" {
		t.Fatalf("first event to Charlie = %q, want welcome", got)
	}

	manager.route(alice, SignalMessage{
		Type:   "offer",
		Target: charlie.ID,
		Data:   json.RawMessage(`{"sdp":"test"}`),
	})
	if got := (<-alice.out).Type; got != "error" {
		t.Fatalf("cross-room routing response = %q, want error", got)
	}
	select {
	case message := <-charlie.out:
		t.Fatalf("cross-room peer received unexpected message: %#v", message)
	default:
	}

	manager.route(alice, SignalMessage{
		Type: "chat",
		Data: json.RawMessage(`{"text":"hello"}`),
	})
	select {
	case message := <-bob.out:
		if message.Type != "chat" {
			t.Fatalf("same-room event = %q, want chat", message.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("same-room participant did not receive chat")
	}
	select {
	case message := <-charlie.out:
		t.Fatalf("chat escaped its room: %#v", message)
	default:
	}
}

func TestRoomManagerEnforcesParticipantLimit(t *testing.T) {
	manager := NewRoomManager()
	for index := 0; index < maxRoomParticipants; index++ {
		peer := testPeer(strconv.Itoa(index), "full-room", "Guest")
		if err := manager.join(peer); err != nil {
			t.Fatalf("join participant %d: %v", index+1, err)
		}
	}
	if err := manager.join(testPeer("overflow", "full-room", "Guest")); err == nil {
		t.Fatal("joining a full room succeeded")
	}
}

func TestValidateSignalMessage(t *testing.T) {
	tests := []struct {
		name    string
		message SignalMessage
		wantErr bool
	}{
		{
			name:    "valid chat",
			message: SignalMessage{Type: "chat", Data: json.RawMessage(`{"text":" hello "}`)},
		},
		{
			name:    "valid presence",
			message: SignalMessage{Type: "presence", Data: json.RawMessage(`{"muted":false,"videoOff":false,"speaking":true}`)},
		},
		{
			name:    "raise hand",
			message: SignalMessage{Type: "raise-hand", Data: json.RawMessage(`{"active":true}`)},
		},
		{
			name:    "presenting",
			message: SignalMessage{Type: "presenting", Data: json.RawMessage(`{"active":false}`)},
		},
		{
			name:    "empty chat",
			message: SignalMessage{Type: "chat", Data: json.RawMessage(`{"text":"  "}`)},
			wantErr: true,
		},
		{
			name:    "oversized chat",
			message: SignalMessage{Type: "chat", Data: marshalJSON(map[string]string{"text": strings.Repeat("x", maxChatRunes+1)})},
			wantErr: true,
		},
		{
			name:    "offer requires target",
			message: SignalMessage{Type: "offer", Data: json.RawMessage(`{"sdp":"test"}`)},
			wantErr: true,
		},
		{
			name:    "unsupported type",
			message: SignalMessage{Type: "new-peer"},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateSignalMessage(&test.message)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateSignalMessage() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestHandleICEServersUsesShortLivedTurnCredentials(t *testing.T) {
	t.Setenv("TURN_URLS", "turn:turn.example.test:3478, turns:turn.example.test:5349")
	t.Setenv("TURN_SHARED_SECRET", "server-only-secret")
	t.Setenv("TURN_CREDENTIAL_TTL", "900")
	t.Setenv("TURN_USERNAME", "")
	t.Setenv("TURN_CREDENTIAL", "")

	req := httptest.NewRequest(http.MethodGet, "/api/ice-servers", nil)
	res := httptest.NewRecorder()
	handleICEServers(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
	}
	if res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", res.Header().Get("Cache-Control"))
	}
	var response struct {
		ICEServers []iceServer `json:"iceServers"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.ICEServers) != 2 || len(response.ICEServers[1].URLs) != 2 {
		t.Fatalf("unexpected ICE server config: %#v", response.ICEServers)
	}
	if strings.Contains(res.Body.String(), "server-only-secret") {
		t.Fatal("shared TURN secret was exposed in the response")
	}
	expiresAt, err := strconv.ParseInt(response.ICEServers[1].Username, 10, 64)
	if err != nil {
		t.Fatalf("TURN username is not an expiry timestamp: %v", err)
	}
	if time.Until(time.Unix(expiresAt, 0)) <= 0 {
		t.Fatal("TURN username has already expired")
	}
	mac := hmac.New(sha1.New, []byte("server-only-secret"))
	_, _ = mac.Write([]byte(response.ICEServers[1].Username))
	wantCredential := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if response.ICEServers[1].Credential != wantCredential {
		t.Fatal("TURN credential does not match the coturn REST signature")
	}
}

func TestHandleICEServersAcceptsTURNURLAlias(t *testing.T) {
	t.Setenv("TURN_URLS", "")
	t.Setenv("TURN_URL", "turn:native.example.test:3478")
	t.Setenv("TURN_SHARED_SECRET", "")
	t.Setenv("TURN_USERNAME", "native-user")
	t.Setenv("TURN_CREDENTIAL", "native-password")

	req := httptest.NewRequest(http.MethodGet, "/api/ice-servers", nil)
	res := httptest.NewRecorder()
	handleICEServers(res, req)

	var response struct {
		ICEServers []iceServer `json:"iceServers"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.ICEServers) != 2 ||
		len(response.ICEServers[1].URLs) != 1 ||
		response.ICEServers[1].URLs[0] != "turn:native.example.test:3478" ||
		response.ICEServers[1].Username != "native-user" ||
		response.ICEServers[1].Credential != "native-password" {
		t.Fatalf("unexpected TURN_URL response: %#v", response.ICEServers)
	}
}

func TestHandleSignalingRejectsMissingRoomOrName(t *testing.T) {
	for _, target := range []string{"/ws?room=test-room", "/ws?room=ab&name=Guest"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		res := httptest.NewRecorder()
		handleSignaling(res, req)
		if res.Code != http.StatusBadRequest {
			t.Errorf("GET %s status = %d, want %d", target, res.Code, http.StatusBadRequest)
		}
	}
}

func TestWebSocketSignalingIsRoomScoped(t *testing.T) {
	handler, err := newHandler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	alice, aliceWelcome := dialRoomPeer(t, server.URL, "ws-room-one", "Alice")
	defer alice.Close()
	if aliceWelcome.Type != "welcome" {
		t.Fatalf("Alice's first message = %q, want welcome", aliceWelcome.Type)
	}
	bob, bobWelcome := dialRoomPeer(t, server.URL, "ws-room-one", "Bob")
	defer bob.Close()
	if bobWelcome.Type != "welcome" || len(bobWelcome.Data) == 0 {
		t.Fatalf("Bob's welcome message is incomplete: %#v", bobWelcome)
	}
	var bobData struct {
		ID    string        `json:"id"`
		Room  string        `json:"room"`
		Peers []Participant `json:"peers"`
	}
	if err := json.Unmarshal(bobWelcome.Data, &bobData); err != nil {
		t.Fatal(err)
	}
	if len(bobData.Peers) != 1 || bobData.Peers[0].Name != "Alice" {
		t.Fatalf("Bob's existing peers = %#v, want Alice", bobData.Peers)
	}
	var newPeer SignalMessage
	if err := alice.ReadJSON(&newPeer); err != nil {
		t.Fatal(err)
	}
	if newPeer.Type != "new-peer" {
		t.Fatalf("Alice's join event = %q, want new-peer", newPeer.Type)
	}

	charlie, charlieWelcome := dialRoomPeer(t, server.URL, "ws-room-two", "Charlie")
	defer charlie.Close()
	if charlieWelcome.Type != "welcome" {
		t.Fatalf("Charlie's first message = %q, want welcome", charlieWelcome.Type)
	}
	var charlieData struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(charlieWelcome.Data, &charlieData); err != nil {
		t.Fatal(err)
	}

	if err := alice.WriteJSON(SignalMessage{
		Type:   "offer",
		Target: charlieData.ID,
		Data:   json.RawMessage(`{"sdp":"room-private"}`),
	}); err != nil {
		t.Fatal(err)
	}
	var routeError SignalMessage
	if err := alice.ReadJSON(&routeError); err != nil {
		t.Fatal(err)
	}
	if routeError.Type != "error" {
		t.Fatalf("cross-room routing response = %q, want error", routeError.Type)
	}

	if err := alice.WriteJSON(SignalMessage{
		Type: "chat",
		Data: json.RawMessage(`{"text":"hello room one"}`),
	}); err != nil {
		t.Fatal(err)
	}
	var chat SignalMessage
	if err := bob.ReadJSON(&chat); err != nil {
		t.Fatal(err)
	}
	if chat.Type != "chat" || chat.Source == "" {
		t.Fatalf("chat broadcast = %#v, want room chat with server source", chat)
	}
	assertNoWebSocketMessage(t, charlie)
}

func dialRoomPeer(t *testing.T, serverURL, room, name string) (*websocket.Conn, SignalMessage) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") +
		"/ws?room=" + room + "&name=" + name
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var welcome SignalMessage
	if err := conn.ReadJSON(&welcome); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	return conn, welcome
}

func assertNoWebSocketMessage(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, _, err := conn.ReadMessage()
	if netErr, ok := err.(interface{ Timeout() bool }); !ok || !netErr.Timeout() {
		t.Fatalf("unexpected WebSocket message or error: %v", err)
	}
}

func testPeer(id, roomID, name string) *Peer {
	return &Peer{
		ID:     id,
		Name:   name,
		RoomID: roomID,
		out:    make(chan SignalMessage, 16),
		done:   make(chan struct{}),
	}
}

func TestProtectedRoomAccessAndLegacyCompatibility(t *testing.T) {
	t.Setenv("LIVEKIT_URL", "wss://livekit.example.test")
	t.Setenv("LIVEKIT_API_KEY", "test-api-key")
	t.Setenv("LIVEKIT_API_SECRET", "test-api-secret")
	t.Setenv("ROOM_ACCESS_SECRET", "test-room-secret")
	t.Setenv("CONFERENCE_TOKEN_RATE_LIMIT", "100")

	roomID, accessCode, err := randomProtectedRoom()
	if err != nil {
		t.Fatal(err)
	}
	if !validRoomID(roomID) {
		t.Fatalf("protected room ID %q does not match the room ID rule", roomID)
	}

	requestToken := func(code string) *httptest.ResponseRecorder {
		t.Helper()
		body := `{"room":"` + roomID + `","username":"Alice","accessCode":"` + code + `","newClientField":{"ignored":true}}`
		req := httptest.NewRequest(http.MethodPost, "/api/conference-token", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handleConferenceToken(res, req)
		return res
	}

	missing := requestToken("")
	if missing.Code != http.StatusForbidden || missing.Body.String() != "access denied\n" {
		t.Fatalf("missing code = %d %q", missing.Code, missing.Body.String())
	}
	wrong := requestToken("WRONG-CODE")
	if wrong.Code != http.StatusForbidden || wrong.Body.String() != missing.Body.String() {
		t.Fatalf("wrong code = %d %q, want same generic 403", wrong.Code, wrong.Body.String())
	}
	correct := requestToken(accessCode)
	if correct.Code != http.StatusOK {
		t.Fatalf("correct code status = %d: %s", correct.Code, correct.Body.String())
	}

	t.Setenv("ROOM_ACCESS_SECRET", "")
	legacy := requestToken("")
	if legacy.Code != http.StatusOK {
		t.Fatalf("legacy room behavior with feature off status = %d: %s", legacy.Code, legacy.Body.String())
	}

	t.Setenv("ROOM_ACCESS_SECRET", "test-room-secret")
	normalRoom := `{"room":"team-room","username":"Alice","accessCode":"not-used","unknown":"accepted"}`
	req := httptest.NewRequest(http.MethodPost, "/api/conference-token", strings.NewReader(normalRoom))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handleConferenceToken(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("normal room with accessCode status = %d: %s", res.Code, res.Body.String())
	}
}

func TestProtectedRoomTamperAndSecretRestartBehavior(t *testing.T) {
	t.Setenv("ROOM_ACCESS_SECRET", "first-secret")
	roomID, accessCode, err := randomProtectedRoom()
	if err != nil {
		t.Fatal(err)
	}
	nonce, protected := protectedRoomNonce(roomID)
	if !protected || nonce == "" {
		t.Fatal("generated room was not recognized as protected")
	}
	if !verifyProtectedRoomAccess(roomID, accessCode) {
		t.Fatal("same secret could not verify generated room")
	}

	tampered := roomID[:len(roomID)-1] + "_"
	if !validRoomID(tampered) {
		tampered = roomID[:len(roomID)-1] + "x"
	}
	if _, protected = protectedRoomNonce(tampered); protected {
		t.Fatal("tampered room ID was still recognized as protected")
	}

	t.Setenv("ROOM_ACCESS_SECRET", "second-secret")
	if verifyProtectedRoomAccess(roomID, accessCode) {
		t.Fatal("different secret accepted a protected room")
	}

	t.Setenv("ROOM_ACCESS_SECRET", "first-secret")
	if !verifyProtectedRoomAccess(roomID, accessCode) {
		t.Fatal("restart with the same secret rejected the room")
	}
}

func TestRoomCreateAndProtectedWebSocketAccess(t *testing.T) {
	if strings.TrimSpace(os.Getenv("DATABASE_URL")) == "" {
		t.Skip("DATABASE_URL is needed for the Neon-backed protected-room integration test")
	}
	t.Setenv("ROOM_ACCESS_SECRET", "test-room-secret")
	t.Setenv("ROOM_CREATE_RATE_LIMIT", "100")
	t.Setenv("ROOM_ACCESS_ATTEMPT_LIMIT", "100")
	t.Setenv("CONFERENCE_TOKEN_RATE_LIMIT", "100")
	t.Setenv("LIVEKIT_URL", "wss://livekit.example.test")
	t.Setenv("LIVEKIT_API_KEY", "test-api-key")
	t.Setenv("LIVEKIT_API_SECRET", "test-api-secret")
	req := httptest.NewRequest(http.MethodPost, "/api/rooms", strings.NewReader(`{"unknownField":true}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handleRoomCreate(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("room create status = %d: %s", res.Code, res.Body.String())
	}
	var created struct {
		Room       string `json:"room"`
		AccessCode string `json:"accessCode"`
		MeetingID  string `json:"meetingId"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !validProtectedRoomV2ID(created.Room) || !validNumericCode(created.MeetingID, meetingIDDigits) ||
		!validNumericCode(created.AccessCode, meetingPasswordDigits) {
		t.Fatalf("created credentials have invalid format: room=%q meetingID=%q", created.Room, created.MeetingID)
	}
	unregisteredRoom, err := randomStoredProtectedRoomID()
	if err != nil {
		t.Fatal(err)
	}
	if !verifyProtectedRoomAccess(unregisteredRoom, "") {
		t.Fatal("an unregistered ID was accidentally classified as protected")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	t.Setenv("DATABASE_URL", "")
	if allowed, err := protectedRoomAccess(created.Room, created.AccessCode); allowed || err == nil {
		t.Fatalf("database outage did not fail closed: allowed=%v err=%v", allowed, err)
	}
	t.Setenv("DATABASE_URL", databaseURL)

	resolve := func(meetingID, password string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]string{
			"meetingId": meetingID,
			"password":  password,
			"ignored":   "accepted",
		})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/rooms/resolve", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handleRoomResolve(response, request)
		return response
	}

	wrongPassword := "999999"
	if wrongPassword == created.AccessCode {
		wrongPassword = "000000"
	}
	wrong := resolve(created.MeetingID, wrongPassword)
	if wrong.Code != http.StatusForbidden || wrong.Body.String() != "access denied\n" {
		t.Fatalf("wrong meeting password = %d %q", wrong.Code, wrong.Body.String())
	}
	unknownID := "0000000000"
	if unknownID == created.MeetingID {
		unknownID = "0000000001"
	}
	unknown := resolve(unknownID, created.AccessCode)
	if unknown.Code != http.StatusForbidden || unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("unknown meeting ID response = %d %q, want generic 403", unknown.Code, unknown.Body.String())
	}
	resolved := resolve(created.MeetingID, created.AccessCode)
	if resolved.Code != http.StatusOK {
		t.Fatalf("resolve status = %d: %s", resolved.Code, resolved.Body.String())
	}
	var resolvedRoom struct {
		Room       string `json:"room"`
		AccessCode string `json:"accessCode"`
	}
	if err := json.Unmarshal(resolved.Body.Bytes(), &resolvedRoom); err != nil {
		t.Fatal(err)
	}
	if resolvedRoom.Room != created.Room || resolvedRoom.AccessCode != created.AccessCode {
		t.Fatalf("resolved credentials do not match created room: %#v", resolvedRoom)
	}

	tokenBody, err := json.Marshal(map[string]string{
		"room":       created.Room,
		"username":   "Alice",
		"accessCode": created.AccessCode,
		"extra":      "accepted",
	})
	if err != nil {
		t.Fatal(err)
	}
	tokenRequest := httptest.NewRequest(http.MethodPost, "/api/conference-token", bytes.NewReader(tokenBody))
	tokenRequest.Header.Set("Content-Type", "application/json")
	tokenResponse := httptest.NewRecorder()
	handleConferenceToken(tokenResponse, tokenRequest)
	if tokenResponse.Code != http.StatusOK {
		t.Fatalf("correct meeting password token status = %d: %s", tokenResponse.Code, tokenResponse.Body.String())
	}
	var issuedToken struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(tokenResponse.Body.Bytes(), &issuedToken); err != nil {
		t.Fatal(err)
	}
	if issuedToken.Token == "" || issuedToken.URL != "wss://livekit.example.test" {
		t.Fatal("protected conference token response is incomplete")
	}

	handler, err := newHandler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	wsBase := "ws" + strings.TrimPrefix(server.URL, "http")
	var firstWebSocketError string
	for _, suffix := range []string{"", "&accessCode=wrong"} {
		conn, response, err := websocket.DefaultDialer.Dial(wsBase+"/ws?room="+created.Room+"&name=Alice"+suffix, nil)
		if err == nil {
			_ = conn.Close()
			t.Fatal("protected WebSocket join unexpectedly succeeded")
		}
		if response == nil {
			t.Fatalf("protected WebSocket rejection has no HTTP response: %v", err)
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusForbidden || string(body) != "access denied\n" {
			t.Fatalf("protected WebSocket rejection = %d %q", response.StatusCode, body)
		}
		if firstWebSocketError == "" {
			firstWebSocketError = string(body)
		} else if string(body) != firstWebSocketError {
			t.Fatalf("WebSocket missing/wrong password bodies differ: %q vs %q", firstWebSocketError, body)
		}
	}

	wsURL := wsBase + "/ws?room=" + created.Room + "&name=Alice&accessCode=" + created.AccessCode
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("correct protected WebSocket join failed: %v", err)
	}
	_ = conn.Close()

	protectedRoomDBState.Lock()
	if protectedRoomDBState.db != nil {
		_ = protectedRoomDBState.db.Close()
	}
	protectedRoomDBState.db = nil
	protectedRoomDBState.dsn = ""
	protectedRoomDBState.Unlock()

	allowed, err := protectedRoomAccess(created.Room, created.AccessCode)
	if err != nil || !allowed {
		t.Fatalf("reopened database rejected created room: allowed=%v err=%v", allowed, err)
	}
	t.Setenv("ROOM_ACCESS_SECRET", "different-secret")
	allowed, err = protectedRoomAccess(created.Room, created.AccessCode)
	if err != nil || allowed {
		t.Fatalf("different secret accepted created room: allowed=%v err=%v", allowed, err)
	}
	t.Setenv("ROOM_ACCESS_SECRET", "")
	allowed, err = protectedRoomAccess(created.Room, created.AccessCode)
	if err != nil || allowed {
		t.Fatalf("feature-off server opened stored protected room: allowed=%v err=%v", allowed, err)
	}
}

func TestNumericMeetingCredentialFormatsAndTampering(t *testing.T) {
	t.Setenv("ROOM_ACCESS_SECRET", "test-room-secret")
	roomID, err := randomStoredProtectedRoomID()
	if err != nil {
		t.Fatal(err)
	}
	if !validRoomID(roomID) || !validProtectedRoomV2ID(roomID) {
		t.Fatalf("generated protected room ID is invalid: %q", roomID)
	}
	if !verifyProtectedRoomAccess("pr2_custom-room", "") {
		t.Fatal("an unregistered custom room using the protected prefix was classified as protected")
	}
	meetingID, err := randomNumericCode(meetingIDDigits)
	if err != nil {
		t.Fatal(err)
	}
	password, err := randomNumericCode(meetingPasswordDigits)
	if err != nil {
		t.Fatal(err)
	}
	if !validNumericCode(meetingID, 10) || !validNumericCode(password, 6) {
		t.Fatalf("generated numeric credentials are malformed: ID=%q password=%q", meetingID, password)
	}

	tampered := roomID[:len(roomID)-1] + "a"
	if tampered == roomID {
		tampered = roomID[:len(roomID)-1] + "b"
	}
	if validProtectedRoomV2ID(tampered) {
		t.Fatal("tampered protected room ID passed its HMAC check")
	}
}

func TestRoomCreateDisabledAndUnknownFields(t *testing.T) {
	t.Setenv("ROOM_ACCESS_SECRET", "")
	req := httptest.NewRequest(http.MethodPost, "/api/rooms", strings.NewReader(`{"ignored":true}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handleRoomCreate(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("room create feature-off status = %d, want 503", res.Code)
	}
}

func TestRateLimiterBurstAndEviction(t *testing.T) {
	limiter := newRateLimiter(3, time.Minute)
	limiter.maxEntries = 4
	now := time.Now()
	for index := 0; index < 3; index++ {
		allowed, _ := limiter.allow("198.51.100.10", now)
		if !allowed {
			t.Fatalf("normal burst request %d was limited", index+1)
		}
	}
	allowed, retryAfter := limiter.allow("198.51.100.10", now)
	if allowed || retryAfter <= 0 {
		t.Fatalf("excessive burst allowed=%v retryAfter=%v", allowed, retryAfter)
	}
	if got := limiter.len(); got != 1 {
		t.Fatalf("limiter entries = %d, want 1", got)
	}
	for index := 0; index < 10; index++ {
		limiter.allow("old-entry-"+strconv.Itoa(index), now)
	}
	limiter.allow("recent-entry", now.Add(3*time.Minute))
	if got := limiter.len(); got > limiter.maxEntries {
		t.Fatalf("limiter map exceeded its configured bound: %d entries", got)
	}
}

func TestConferenceTokenReturnsRateLimitWithRetryAfter(t *testing.T) {
	t.Setenv("LIVEKIT_URL", "wss://livekit.example.test")
	t.Setenv("LIVEKIT_API_KEY", "test-api-key")
	t.Setenv("LIVEKIT_API_SECRET", "test-api-secret")
	t.Setenv("CONFERENCE_TOKEN_RATE_LIMIT", "2")
	t.Setenv("CONFERENCE_TOKEN_RATE_WINDOW_SECONDS", "60")

	for index := 0; index < 3; index++ {
		req := httptest.NewRequest(http.MethodPost, "/api/conference-token",
			strings.NewReader(`{"room":"team-room","username":"Alice"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "198.51.100.20:12345"
		res := httptest.NewRecorder()
		handleConferenceToken(res, req)
		if index < 2 {
			if res.Code != http.StatusOK {
				t.Fatalf("normal burst request %d status = %d: %s", index+1, res.Code, res.Body.String())
			}
		} else {
			if res.Code != http.StatusTooManyRequests {
				t.Fatalf("excessive burst status = %d, want 429", res.Code)
			}
			if res.Header().Get("Retry-After") == "" {
				t.Fatal("429 response missing Retry-After")
			}
			if res.Body.String() != "too many requests\n" {
				t.Fatalf("429 body = %q", res.Body.String())
			}
		}
	}
}
