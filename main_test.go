package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

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
