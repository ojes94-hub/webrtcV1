package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const hostTestDriverName = "webrtc-host-controls-test"

var (
	hostTestDriverOnce sync.Once
	hostTestStores     sync.Map
	hostTestDSNCount   atomic.Uint64
)

type hostTestRoomRow struct {
	meetingID    string
	roomID       string
	passwordHash []byte
	hostKeyHash  []byte
	endedAt      sql.NullTime
}

type hostTestStore struct {
	mu    sync.Mutex
	rooms map[string]hostTestRoomRow
}

type hostTestDriver struct{}

type hostTestConn struct {
	store    *hostTestStore
	activeTx *hostTestTx
}

type hostTestTx struct {
	conn         *hostTestConn
	pendingEnded map[string]time.Time
	closed       bool
}

type hostTestRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

type hostTestResult int64

func (hostTestResult) LastInsertId() (int64, error) { return 0, nil }
func (result hostTestResult) RowsAffected() (int64, error) {
	return int64(result), nil
}

func (hostTestRows) Close() error           { return nil }
func (rows hostTestRows) Columns() []string { return rows.columns }
func (rows *hostTestRows) Next(dest []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(dest, rows.values[rows.index])
	rows.index++
	return nil
}

func (hostTestDriver) Open(name string) (driver.Conn, error) {
	store, ok := hostTestStores.Load(name)
	if !ok {
		return nil, errors.New("unknown host-controls test database")
	}
	return &hostTestConn{store: store.(*hostTestStore)}, nil
}

func (conn *hostTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported")
}
func (conn *hostTestConn) Close() error { return nil }
func (conn *hostTestConn) Begin() (driver.Tx, error) {
	return conn.BeginTx(context.Background(), driver.TxOptions{})
}
func (conn *hostTestConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	tx := &hostTestTx{conn: conn, pendingEnded: make(map[string]time.Time)}
	conn.activeTx = tx
	return tx, nil
}

func (conn *hostTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if conn.activeTx != nil && strings.Contains(query, "UPDATE protected_rooms SET ended_at") {
		return conn.activeTx.execUpdate(query, args)
	}
	if strings.Contains(query, "INSERT INTO protected_rooms") {
		meetingID := args[0].Value.(string)
		roomID := args[1].Value.(string)
		passwordHash := append([]byte(nil), args[2].Value.([]byte)...)
		hostKeyHash := append([]byte(nil), args[3].Value.([]byte)...)
		conn.store.mu.Lock()
		defer conn.store.mu.Unlock()
		if _, exists := conn.store.rooms[roomID]; exists {
			return hostTestResult(0), nil
		}
		for _, room := range conn.store.rooms {
			if room.meetingID == meetingID {
				return hostTestResult(0), nil
			}
		}
		conn.store.rooms[roomID] = hostTestRoomRow{
			meetingID: meetingID, roomID: roomID,
			passwordHash: passwordHash, hostKeyHash: hostKeyHash,
		}
		return hostTestResult(1), nil
	}
	return nil, fmt.Errorf("unsupported test SQL: %s", query)
}

func (conn *hostTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	roomID := args[0].Value.(string)
	conn.store.mu.Lock()
	room, exists := conn.store.rooms[roomID]
	conn.store.mu.Unlock()
	if !exists {
		if strings.Contains(query, "SELECT password_hash") {
			return &hostTestRows{columns: []string{"password_hash"}}, nil
		}
		return &hostTestRows{columns: []string{"host_key_hash", "ended_at"}}, nil
	}
	if strings.Contains(query, "SELECT password_hash") {
		return &hostTestRows{
			columns: []string{"password_hash"},
			values:  [][]driver.Value{{append([]byte(nil), room.passwordHash...)}},
		}, nil
	}
	if strings.Contains(query, "SELECT host_key_hash, ended_at") {
		var endedAt driver.Value
		if room.endedAt.Valid {
			endedAt = room.endedAt.Time
		}
		return &hostTestRows{
			columns: []string{"host_key_hash", "ended_at"},
			values: [][]driver.Value{{
				append([]byte(nil), room.hostKeyHash...), endedAt,
			}},
		}, nil
	}
	return nil, fmt.Errorf("unsupported test SQL: %s", query)
}

func (tx *hostTestTx) Commit() error {
	if tx.closed {
		return sql.ErrTxDone
	}
	tx.conn.store.mu.Lock()
	for roomID, endedAt := range tx.pendingEnded {
		room := tx.conn.store.rooms[roomID]
		room.endedAt = sql.NullTime{Time: endedAt, Valid: true}
		tx.conn.store.rooms[roomID] = room
	}
	tx.conn.store.mu.Unlock()
	tx.closed = true
	tx.conn.activeTx = nil
	return nil
}

func (tx *hostTestTx) Rollback() error {
	if tx.closed {
		return sql.ErrTxDone
	}
	tx.closed = true
	tx.conn.activeTx = nil
	return nil
}

func (tx *hostTestTx) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if tx.closed {
		return nil, sql.ErrTxDone
	}
	return tx.execUpdate(query, args)
}

func (tx *hostTestTx) execUpdate(query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "UPDATE protected_rooms SET ended_at") {
		roomID := args[0].Value.(string)
		tx.conn.store.mu.Lock()
		room, exists := tx.conn.store.rooms[roomID]
		tx.conn.store.mu.Unlock()
		if !exists || len(room.hostKeyHash) == 0 || room.endedAt.Valid {
			return hostTestResult(0), nil
		}
		tx.pendingEnded[roomID] = time.Now()
		return hostTestResult(1), nil
	}
	return nil, fmt.Errorf("unsupported test transaction SQL: %s", query)
}

func (tx *hostTestTx) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return tx.conn.QueryContext(ctx, query, args)
}

func newHostTestDatabase(t *testing.T) *hostTestStore {
	t.Helper()
	hostTestDriverOnce.Do(func() {
		sql.Register(hostTestDriverName, hostTestDriver{})
	})
	t.Setenv("ROOM_ACCESS_SECRET", "room-access-test-secret")
	dsn := fmt.Sprintf("host-test-%d", hostTestDSNCount.Add(1))
	store := &hostTestStore{rooms: make(map[string]hostTestRoomRow)}
	hostTestStores.Store(dsn, store)
	db, err := sql.Open(hostTestDriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", dsn)

	protectedRoomDBState.Lock()
	oldDB, oldDSN := protectedRoomDBState.db, protectedRoomDBState.dsn
	protectedRoomDBState.db, protectedRoomDBState.dsn = db, dsn
	protectedRoomDBState.Unlock()
	t.Cleanup(func() {
		protectedRoomDBState.Lock()
		currentDB := protectedRoomDBState.db
		protectedRoomDBState.db, protectedRoomDBState.dsn = oldDB, oldDSN
		protectedRoomDBState.Unlock()
		if currentDB != nil && currentDB != oldDB {
			_ = currentDB.Close()
		}
		hostTestStores.Delete(dsn)
	})
	t.Setenv("ROOM_CREATE_RATE_LIMIT", "100")
	t.Setenv("ROOM_MODERATE_RATE_LIMIT", "100")
	t.Setenv("ROOM_MODERATE_RATE_WINDOW_SECONDS", "60")
	t.Setenv("CONFERENCE_TOKEN_RATE_LIMIT", "100")
	t.Setenv("ROOM_ACCESS_ATTEMPT_LIMIT", "100")
	return store
}

func addHostTestRoom(t *testing.T, store *hostTestStore, hostEnabled bool) (string, string, string) {
	t.Helper()
	roomID, err := randomStoredProtectedRoomID()
	if err != nil {
		t.Fatal(err)
	}
	meetingID, err := randomNumericCode(meetingIDDigits)
	if err != nil {
		t.Fatal(err)
	}
	password, err := randomNumericCode(meetingPasswordDigits)
	if err != nil {
		t.Fatal(err)
	}
	var hostKey string
	var hostHash []byte
	if hostEnabled {
		hostKey, err = randomHostKey()
		if err != nil {
			t.Fatal(err)
		}
		hostHash = protectedRoomHostKeyHash(roomID, hostKey)
	}
	store.mu.Lock()
	store.rooms[roomID] = hostTestRoomRow{
		meetingID: meetingID, roomID: roomID,
		passwordHash: protectedRoomPasswordHash(roomID, password), hostKeyHash: hostHash,
	}
	store.mu.Unlock()
	return roomID, password, hostKey
}

func TestHostKeyCreationAndConferenceTokenCompatibility(t *testing.T) {
	store := newHostTestDatabase(t)
	t.Setenv("LIVEKIT_URL", "wss://livekit.example.test")
	t.Setenv("LIVEKIT_API_KEY", "test-api-key")
	t.Setenv("LIVEKIT_API_SECRET", "test-api-secret")
	var logs bytes.Buffer
	oldLogWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldLogWriter) })

	createRequest := httptest.NewRequest(http.MethodPost, "/api/rooms",
		strings.NewReader(`{"newClientField":"accepted"}`))
	createRequest.Header.Set("Content-Type", "application/json")
	createResponse := httptest.NewRecorder()
	handleRoomCreate(createResponse, createRequest)
	if createResponse.Code != http.StatusOK {
		t.Fatalf("room creation status = %d: %s", createResponse.Code, createResponse.Body.String())
	}
	var created struct {
		Room       string `json:"room"`
		AccessCode string `json:"accessCode"`
		MeetingID  string `json:"meetingId"`
		HostKey    string `json:"hostKey"`
	}
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	var responseFields map[string]json.RawMessage
	if err := json.Unmarshal(createResponse.Body.Bytes(), &responseFields); err != nil {
		t.Fatal(err)
	}
	if len(responseFields) != 4 {
		t.Fatalf("room creation fields = %v, want existing fields plus hostKey", responseFields)
	}
	hostKeyBytes, err := base64.RawURLEncoding.DecodeString(created.HostKey)
	if err != nil || len(hostKeyBytes) != 32 || strings.Contains(created.HostKey, "=") {
		t.Fatalf("host key is not 32-byte unpadded base64url: %q", created.HostKey)
	}
	store.mu.Lock()
	stored := store.rooms[created.Room]
	store.mu.Unlock()
	if len(stored.hostKeyHash) != sha256.Size || bytes.Contains(stored.hostKeyHash, hostKeyBytes) {
		t.Fatal("host key was not stored as a 32-byte keyed hash")
	}

	callToken := func(room, accessCode string, hostKey *string, get bool) *httptest.ResponseRecorder {
		t.Helper()
		var request *http.Request
		if get {
			target := "/api/conference-token?room=" + room + "&username=Alice&accessCode=" + accessCode
			if hostKey != nil {
				target += "&hostKey=" + *hostKey
			}
			request = httptest.NewRequest(http.MethodGet, target, nil)
		} else {
			body := map[string]string{"room": room, "username": "Alice", "accessCode": accessCode, "future": "accepted"}
			if hostKey != nil {
				body["hostKey"] = *hostKey
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			request = httptest.NewRequest(http.MethodPost, "/api/conference-token", bytes.NewReader(encoded))
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		handleConferenceToken(response, request)
		return response
	}

	plain := callToken(created.Room, created.AccessCode, nil, false)
	if plain.Code != http.StatusOK {
		t.Fatalf("token without host key status = %d: %s", plain.Code, plain.Body.String())
	}
	plainToken := decodeTokenFromResponse(t, plain)
	payload := tokenPayloadFromResponse(t, plain)
	var plainClaims conferenceTokenClaims
	if err := json.Unmarshal(payload, &plainClaims); err != nil {
		t.Fatal(err)
	}
	legacyPayload, err := json.Marshal(struct {
		Issuer    string               `json:"iss"`
		Subject   string               `json:"sub"`
		Name      string               `json:"name"`
		IssuedAt  int64                `json:"iat"`
		NotBefore int64                `json:"nbf"`
		ExpiresAt int64                `json:"exp"`
		TokenID   string               `json:"jti"`
		Video     conferenceVideoGrant `json:"video"`
	}{
		Issuer: plainClaims.Issuer, Subject: plainClaims.Subject, Name: plainClaims.Name,
		IssuedAt: plainClaims.IssuedAt, NotBefore: plainClaims.NotBefore,
		ExpiresAt: plainClaims.ExpiresAt, TokenID: plainClaims.TokenID, Video: plainClaims.Video,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, legacyPayload) {
		t.Fatalf("claims without hostKey differ from the legacy token payload: %s", payload)
	}
	if _, ok := plainToken["metadata"]; ok {
		t.Fatal("participant token without host key contains metadata")
	}
	var participantVideo map[string]json.RawMessage
	if err := json.Unmarshal(plainToken["video"], &participantVideo); err != nil {
		t.Fatal(err)
	}
	for _, grant := range []string{"roomAdmin", "roomCreate", "roomList"} {
		if _, ok := participantVideo[grant]; ok {
			t.Fatalf("participant token unexpectedly contains %s grant", grant)
		}
	}

	host := callToken(created.Room, created.AccessCode, &created.HostKey, false)
	if host.Code != http.StatusOK {
		t.Fatalf("token with host key status = %d: %s", host.Code, host.Body.String())
	}
	hostClaims := decodeTokenFromResponse(t, host)
	var metadata string
	if err := json.Unmarshal(hostClaims["metadata"], &metadata); err != nil || metadata != `{"role":"host"}` {
		t.Fatalf("host metadata = %q, err=%v", metadata, err)
	}
	if getResponse := callToken(created.Room, created.AccessCode, &created.HostKey, true); getResponse.Code != http.StatusOK {
		t.Fatalf("GET token with host key status = %d: %s", getResponse.Code, getResponse.Body.String())
	}
	wrongKey := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	for _, tc := range []struct {
		name string
		room string
		key  string
	}{
		{name: "invalid key", room: created.Room, key: wrongKey},
	} {
		response := callToken(tc.room, created.AccessCode, &tc.key, false)
		if response.Code != http.StatusForbidden || response.Body.String() != "access denied\n" {
			t.Fatalf("%s response = %d %q", tc.name, response.Code, response.Body.String())
		}
	}
	otherRoom, otherCode, _ := addHostTestRoom(t, store, true)
	wrongRoom := callToken(otherRoom, otherCode, &created.HostKey, false)
	if wrongRoom.Code != http.StatusForbidden || wrongRoom.Body.String() != "access denied\n" {
		t.Fatalf("host key for another room response = %d %q", wrongRoom.Code, wrongRoom.Body.String())
	}
	if response := callToken(created.Room, created.AccessCode, stringPointer(""), false); response.Code != http.StatusForbidden {
		t.Fatalf("empty host key response = %d %q", response.Code, response.Body.String())
	}
	nullKeyBody, err := json.Marshal(map[string]any{
		"room": created.Room, "username": "Alice", "accessCode": created.AccessCode, "hostKey": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	nullKeyRequest := httptest.NewRequest(http.MethodPost, "/api/conference-token", bytes.NewReader(nullKeyBody))
	nullKeyRequest.Header.Set("Content-Type", "application/json")
	nullKeyResponse := httptest.NewRecorder()
	handleConferenceToken(nullKeyResponse, nullKeyRequest)
	if nullKeyResponse.Code != http.StatusForbidden || nullKeyResponse.Body.String() != "access denied\n" {
		t.Fatalf("null host key response = %d %q", nullKeyResponse.Code, nullKeyResponse.Body.String())
	}

	legacyRoom, legacyCode, _ := addHostTestRoom(t, store, false)
	if response := callToken(legacyRoom, legacyCode, nil, false); response.Code != http.StatusOK {
		t.Fatalf("pre-existing protected room token status = %d: %s", response.Code, response.Body.String())
	}
	if response := callToken(legacyRoom, legacyCode, &created.HostKey, false); response.Code != http.StatusForbidden ||
		response.Body.String() != "access denied\n" {
		t.Fatalf("pre-existing room with host key response = %d %q", response.Code, response.Body.String())
	}
	pr1Room, pr1AccessCode, err := randomProtectedRoom()
	if err != nil {
		t.Fatal(err)
	}
	if response := callToken(pr1Room, pr1AccessCode, nil, false); response.Code != http.StatusOK {
		t.Fatalf("pre-existing pr1 room token status = %d: %s", response.Code, response.Body.String())
	}
	if response := callToken(pr1Room, pr1AccessCode, &created.HostKey, false); response.Code != http.StatusForbidden ||
		response.Body.String() != "access denied\n" {
		t.Fatalf("pr1 room with host key response = %d %q", response.Code, response.Body.String())
	}
	if response := callToken("team-room", "", &created.HostKey, false); response.Code != http.StatusForbidden ||
		response.Body.String() != "access denied\n" {
		t.Fatalf("unprotected room with host key response = %d %q", response.Code, response.Body.String())
	}
	if response := callToken("team-room", "", nil, false); response.Code != http.StatusOK {
		t.Fatalf("unprotected room without host key status = %d: %s", response.Code, response.Body.String())
	}

	if strings.Contains(logs.String(), created.HostKey) || strings.Contains(logs.String(), created.AccessCode) ||
		strings.Contains(logs.String(), "test-api-secret") || strings.Contains(logs.String(), plainTokenString(plain)) {
		t.Fatal("sensitive values were written to logs")
	}
}

func decodeTokenFromResponse(t *testing.T, response *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	payload := tokenPayloadFromResponse(t, response)
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func tokenPayloadFromResponse(t *testing.T, response *httptest.ResponseRecorder) []byte {
	t.Helper()
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(body.Token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func stringPointer(value string) *string {
	return &value
}

func plainTokenString(response *httptest.ResponseRecorder) string {
	var body struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return body.Token
}

type fakeLiveKitCall struct {
	method string
	body   map[string]json.RawMessage
}

type fakeLiveKitServer struct {
	server       *httptest.Server
	mu           sync.Mutex
	calls        []fakeLiveKitCall
	participants []liveKitParticipant
	failMethod   string
	failStatus   int
}

func newFakeLiveKitServer(t *testing.T, roomID string, participants []liveKitParticipant) *fakeLiveKitServer {
	t.Helper()
	fake := &fakeLiveKitServer{participants: participants}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		method := strings.TrimPrefix(r.URL.Path, "/twirp/livekit.RoomService/")
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		assertLiveKitAdminToken(t, token, "test-api-key", "test-api-secret", roomID)
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		var requestRoom string
		if err := json.Unmarshal(body["room"], &requestRoom); err != nil || requestRoom != roomID {
			t.Errorf("%s request room = %q, err=%v; want %q", method, requestRoom, err, roomID)
		}
		switch method {
		case "ListParticipants", "DeleteRoom":
			if len(body) != 1 {
				t.Errorf("%s request body = %v, want only room", method, body)
			}
		case "MutePublishedTrack":
			for _, field := range []string{"identity", "trackSid", "muted"} {
				if _, ok := body[field]; !ok {
					t.Errorf("MutePublishedTrack request missing %s: %v", field, body)
				}
			}
		case "RemoveParticipant":
			if _, ok := body["identity"]; !ok || len(body) != 2 {
				t.Errorf("RemoveParticipant request body = %v, want room and identity", body)
			}
		default:
			t.Errorf("unexpected Twirp method %q", method)
		}
		fake.mu.Lock()
		fake.calls = append(fake.calls, fakeLiveKitCall{method: method, body: body})
		failMethod, failStatus := fake.failMethod, fake.failStatus
		participants := fake.participants
		fake.mu.Unlock()
		if method == failMethod {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(failStatus)
			_, _ = io.WriteString(w, `{"code":"internal","msg":"failure"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "ListParticipants":
			_ = json.NewEncoder(w).Encode(liveKitParticipantsResponse{Participants: participants})
		default:
			_, _ = io.WriteString(w, "{}")
		}
	}))
	t.Cleanup(fake.server.Close)
	t.Setenv("LIVEKIT_URL", fake.server.URL)
	t.Setenv("LIVEKIT_API_KEY", "test-api-key")
	t.Setenv("LIVEKIT_API_SECRET", "test-api-secret")
	return fake
}

func assertLiveKitAdminToken(t *testing.T, token, apiKey, apiSecret, roomID string) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("admin token has %d segments", len(parts))
	}
	mac := hmac.New(sha256.New, []byte(apiSecret))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		t.Fatal("admin JWT signature is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Issuer    string `json:"iss"`
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Video     struct {
			Room       string          `json:"room"`
			RoomAdmin  bool            `json:"roomAdmin"`
			RoomCreate bool            `json:"roomCreate"`
			RoomList   json.RawMessage `json:"roomList"`
		} `json:"video"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != apiKey || claims.ExpiresAt-claims.IssuedAt > 60 || claims.ExpiresAt <= claims.IssuedAt ||
		claims.Video.Room != roomID || !claims.Video.RoomAdmin || !claims.Video.RoomCreate || len(claims.Video.RoomList) != 0 {
		t.Fatalf("admin JWT has unexpected claims: %+v", claims)
	}
}

func mustJSONRaw(value string) json.RawMessage {
	return json.RawMessage(value)
}

func TestHostModerationActionsAndEndedRoomPersistence(t *testing.T) {
	store := newHostTestDatabase(t)
	roomID, accessCode, hostKey := addHostTestRoom(t, store, true)
	participants := []liveKitParticipant{
		{Identity: "host", Metadata: `{"role":"host"}`, Tracks: []liveKitTrack{{SID: "host-mic", Source: mustJSONRaw(`"MICROPHONE"`)}}},
		{Identity: "alice", Tracks: []liveKitTrack{
			{SID: "alice-mic", Source: mustJSONRaw(`"MICROPHONE"`)},
			{SID: "alice-camera", Source: mustJSONRaw(`"CAMERA"`)},
		}},
		{Identity: "bob", Tracks: []liveKitTrack{{SID: "bob-mic", Source: mustJSONRaw(`"MICROPHONE"`)}}},
	}
	fake := newFakeLiveKitServer(t, roomID, participants)
	requestModerate := func(action, identity string) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(map[string]string{
			"room": roomID, "hostKey": hostKey, "action": action, "identity": identity,
		})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/rooms/moderate", bytes.NewReader(encoded))
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = "198.51.100.40:12345"
		response := httptest.NewRecorder()
		handleRoomModerate(response, request)
		return response
	}
	assertSuccess := func(response *httptest.ResponseRecorder, affected int) {
		t.Helper()
		if response.Code != http.StatusOK {
			t.Fatalf("moderation status = %d: %s", response.Code, response.Body.String())
		}
		var body struct {
			OK       bool `json:"ok"`
			Affected int  `json:"affected"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !body.OK || body.Affected != affected {
			t.Fatalf("moderation response = %+v, want affected %d", body, affected)
		}
	}

	assertSuccess(requestModerate("mute", "alice"), 1)
	fake.mu.Lock()
	if len(fake.calls) != 2 || fake.calls[0].method != "ListParticipants" || fake.calls[1].method != "MutePublishedTrack" {
		t.Fatalf("mute calls = %+v", fake.calls)
	}
	var muteBody struct {
		Room     string `json:"room"`
		Identity string `json:"identity"`
		TrackSID string `json:"trackSid"`
		Muted    bool   `json:"muted"`
	}
	if err := json.Unmarshal(rawJSONMap(fake.calls[1].body), &muteBody); err != nil {
		fake.mu.Unlock()
		t.Fatal(err)
	}
	if muteBody.Room != roomID || muteBody.Identity != "alice" || muteBody.TrackSID != "alice-mic" || !muteBody.Muted {
		fake.mu.Unlock()
		t.Fatalf("mute request body = %+v", muteBody)
	}
	fake.calls = nil
	fake.mu.Unlock()

	assertSuccess(requestModerate("mute_all", ""), 2)
	fake.mu.Lock()
	if len(fake.calls) != 3 || fake.calls[0].method != "ListParticipants" ||
		fake.calls[1].method != "MutePublishedTrack" || fake.calls[2].method != "MutePublishedTrack" {
		t.Fatalf("mute_all calls = %+v", fake.calls)
	}
	mutedTracks := make(map[string]bool)
	for _, call := range fake.calls[1:] {
		var body struct {
			Identity string `json:"identity"`
			TrackSID string `json:"trackSid"`
		}
		if err := json.Unmarshal(rawJSONMap(call.body), &body); err != nil {
			fake.mu.Unlock()
			t.Fatal(err)
		}
		mutedTracks[body.Identity+":"+body.TrackSID] = true
	}
	fake.calls = nil
	fake.mu.Unlock()
	if !mutedTracks["alice:alice-mic"] || !mutedTracks["bob:bob-mic"] || len(mutedTracks) != 2 {
		t.Fatalf("mute_all muted tracks = %v", mutedTracks)
	}
	ignoredIdentityBody, err := json.Marshal(map[string]any{
		"room": roomID, "hostKey": hostKey, "action": "mute_all", "identity": map[string]any{"ignored": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	ignoredIdentityRequest := httptest.NewRequest(http.MethodPost, "/api/rooms/moderate", bytes.NewReader(ignoredIdentityBody))
	ignoredIdentityRequest.Header.Set("Content-Type", "application/json")
	ignoredIdentityRequest.RemoteAddr = "198.51.100.40:12345"
	ignoredIdentityResponse := httptest.NewRecorder()
	handleRoomModerate(ignoredIdentityResponse, ignoredIdentityRequest)
	assertSuccess(ignoredIdentityResponse, 2)
	fake.mu.Lock()
	fake.calls = nil
	fake.mu.Unlock()

	assertSuccess(requestModerate("remove", "alice"), 1)
	fake.mu.Lock()
	if len(fake.calls) != 2 || fake.calls[1].method != "RemoveParticipant" {
		t.Fatalf("remove calls = %+v", fake.calls)
	}
	var removeBody struct {
		Room     string `json:"room"`
		Identity string `json:"identity"`
	}
	if err := json.Unmarshal(rawJSONMap(fake.calls[1].body), &removeBody); err != nil {
		fake.mu.Unlock()
		t.Fatal(err)
	}
	if removeBody.Room != roomID || removeBody.Identity != "alice" {
		fake.mu.Unlock()
		t.Fatalf("remove request body = %+v", removeBody)
	}
	fake.calls = nil
	fake.mu.Unlock()

	self := requestModerate("mute", "host")
	if self.Code != http.StatusForbidden || self.Body.String() != "access denied\n" {
		t.Fatalf("host mute response = %d %q", self.Code, self.Body.String())
	}
	self = requestModerate("remove", "host")
	if self.Code != http.StatusForbidden || self.Body.String() != "access denied\n" {
		t.Fatalf("host removal response = %d %q", self.Code, self.Body.String())
	}
	fake.mu.Lock()
	fake.calls = nil
	fake.mu.Unlock()

	assertSuccess(requestModerate("end", ""), 1)
	fake.mu.Lock()
	if len(fake.calls) != 1 || fake.calls[0].method != "DeleteRoom" {
		t.Fatalf("end calls = %+v", fake.calls)
	}
	var deleteBody struct {
		Room string `json:"room"`
	}
	if err := json.Unmarshal(rawJSONMap(fake.calls[0].body), &deleteBody); err != nil {
		fake.mu.Unlock()
		t.Fatal(err)
	}
	fake.mu.Unlock()
	if deleteBody.Room != roomID {
		t.Fatalf("delete request room = %q, want %q", deleteBody.Room, roomID)
	}

	reopenHostTestDatabase(t)
	handler, err := newHandler()
	if err != nil {
		t.Fatal(err)
	}
	tokenBody, err := json.Marshal(map[string]string{
		"room": roomID, "username": "Alice", "accessCode": accessCode,
	})
	if err != nil {
		t.Fatal(err)
	}
	tokenRequest := httptest.NewRequest(http.MethodPost, "/api/conference-token", bytes.NewReader(tokenBody))
	tokenRequest.Header.Set("Content-Type", "application/json")
	tokenResponse := httptest.NewRecorder()
	handler.ServeHTTP(tokenResponse, tokenRequest)
	if tokenResponse.Code != http.StatusGone || tokenResponse.Body.String() != "meeting ended\n" {
		t.Fatalf("token after restart = %d %q", tokenResponse.Code, tokenResponse.Body.String())
	}
	if ended := requestModerate("mute_all", ""); ended.Code != http.StatusGone || ended.Body.String() != "meeting ended\n" {
		t.Fatalf("moderation after end = %d %q", ended.Code, ended.Body.String())
	}
}

func rawJSONMap(values map[string]json.RawMessage) []byte {
	encoded, _ := json.Marshal(values)
	return encoded
}

func reopenHostTestDatabase(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	protectedRoomDBState.Lock()
	oldDB := protectedRoomDBState.db
	protectedRoomDBState.db = nil
	protectedRoomDBState.dsn = ""
	protectedRoomDBState.Unlock()
	if oldDB != nil {
		if err := oldDB.Close(); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open(hostTestDriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	protectedRoomDBState.Lock()
	protectedRoomDBState.db = db
	protectedRoomDBState.dsn = dsn
	protectedRoomDBState.Unlock()
}

func TestHostModerationAuthorizationStatusesAndLiveKitFailures(t *testing.T) {
	store := newHostTestDatabase(t)
	roomID, _, hostKey := addHostTestRoom(t, store, true)
	legacyRoom, _, _ := addHostTestRoom(t, store, false)
	request := func(room, key, action, identity string, ip string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]string{
			"room": room, "hostKey": key, "action": action, "identity": identity,
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/rooms/moderate", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = ip
		res := httptest.NewRecorder()
		handleRoomModerate(res, req)
		return res
	}

	for _, tc := range []struct {
		name string
		room string
		key  string
	}{
		{name: "missing key", room: roomID, key: ""},
		{name: "wrong key", room: roomID, key: strings.Repeat("x", 43)},
		{name: "legacy protected room", room: legacyRoom, key: hostKey},
		{name: "normal room", room: "normal-room", key: hostKey},
	} {
		response := request(tc.room, tc.key, "mute_all", "", "198.51.100.51:12345")
		if response.Code != http.StatusForbidden || response.Body.String() != "access denied\n" {
			t.Fatalf("%s response = %d %q", tc.name, response.Code, response.Body.String())
		}
	}

	t.Setenv("LIVEKIT_URL", "")
	t.Setenv("LIVEKIT_API_KEY", "")
	t.Setenv("LIVEKIT_API_SECRET", "")
	unconfigured := request(roomID, hostKey, "mute_all", "", "198.51.100.52:12345")
	if unconfigured.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured LiveKit status = %d: %s", unconfigured.Code, unconfigured.Body.String())
	}

	fake := newFakeLiveKitServer(t, roomID, nil)
	fake.failMethod = "ListParticipants"
	fake.failStatus = http.StatusInternalServerError
	failed := request(roomID, hostKey, "mute_all", "", "198.51.100.53:12345")
	if failed.Code != http.StatusBadGateway {
		t.Fatalf("LiveKit failure status = %d: %s", failed.Code, failed.Body.String())
	}

	fake.failMethod = ""
	notFound := request(roomID, hostKey, "mute", "missing", "198.51.100.54:12345")
	if notFound.Code != http.StatusNotFound || notFound.Body.String() != "participant not found\n" {
		t.Fatalf("missing participant response = %d %q", notFound.Code, notFound.Body.String())
	}

	t.Setenv("ROOM_MODERATE_RATE_LIMIT", "1")
	t.Setenv("ROOM_MODERATE_RATE_WINDOW_SECONDS", "60")
	first := request(roomID, "wrong", "mute_all", "", "198.51.100.55:12345")
	second := request(roomID, "wrong", "mute_all", "", "198.51.100.55:12345")
	if first.Code != http.StatusForbidden {
		t.Fatalf("first rate-limit request = %d %q", first.Code, first.Body.String())
	}
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("rate-limited request = %d headers=%v body=%q", second.Code, second.Header(), second.Body.String())
	}
}
