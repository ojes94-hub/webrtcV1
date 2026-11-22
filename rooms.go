package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	maxRoomParticipants = 10
	maxMessageBytes     = 64 * 1024
	maxChatRunes        = 2000
	writeWait           = 10 * time.Second
	pongWait            = 60 * time.Second
	pingPeriod          = 54 * time.Second
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		parsed, err := url.Parse(origin)
		return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host == r.Host
	},
}

type SignalMessage struct {
	Type   string          `json:"type"`
	Source string          `json:"source,omitempty"`
	Target string          `json:"target,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

type Participant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Peer struct {
	ID     string
	Name   string
	RoomID string
	Conn   *websocket.Conn
	out    chan SignalMessage
	done   chan struct{}
	once   sync.Once
}

type RoomManager struct {
	mu    sync.Mutex
	rooms map[string]map[string]*Peer
}

var roomManager = NewRoomManager()

func NewRoomManager() *RoomManager {
	return &RoomManager{rooms: make(map[string]map[string]*Peer)}
}

func validRoomID(roomID string) bool {
	if len(roomID) < 3 || len(roomID) > 64 {
		return false
	}
	for _, char := range roomID {
		if !(char >= 'a' && char <= 'z') &&
			!(char >= 'A' && char <= 'Z') &&
			!(char >= '0' && char <= '9') &&
			char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func cleanDisplayName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 40 {
		return "", errors.New("display name must be between 1 and 40 characters")
	}
	for _, char := range name {
		if unicode.IsControl(char) {
			return "", errors.New("display name contains unsupported characters")
		}
	}
	return name, nil
}

func (m *RoomManager) join(peer *Peer) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	members := m.rooms[peer.RoomID]
	if members == nil {
		members = make(map[string]*Peer)
		m.rooms[peer.RoomID] = members
	}
	if len(members) >= maxRoomParticipants {
		return errors.New("room is full")
	}

	existing := make([]Participant, 0, len(members))
	for _, member := range members {
		existing = append(existing, Participant{ID: member.ID, Name: member.Name})
	}
	sort.Slice(existing, func(i, j int) bool { return existing[i].ID < existing[j].ID })

	welcomeData, err := json.Marshal(struct {
		ID    string        `json:"id"`
		Room  string        `json:"room"`
		Peers []Participant `json:"peers"`
	}{ID: peer.ID, Room: peer.RoomID, Peers: existing})
	if err != nil {
		return err
	}
	if !peer.enqueue(SignalMessage{Type: "welcome", Data: welcomeData}) {
		return errors.New("unable to initialize peer connection")
	}

	members[peer.ID] = peer
	newPeerData, _ := json.Marshal(Participant{ID: peer.ID, Name: peer.Name})
	for _, member := range members {
		if member.ID != peer.ID {
			member.enqueue(SignalMessage{Type: "new-peer", Source: peer.ID, Data: newPeerData})
		}
	}
	return nil
}

func (m *RoomManager) leave(peer *Peer) {
	m.mu.Lock()
	members := m.rooms[peer.RoomID]
	if members == nil || members[peer.ID] != peer {
		m.mu.Unlock()
		return
	}
	delete(members, peer.ID)
	if len(members) == 0 {
		delete(m.rooms, peer.RoomID)
	}
	remaining := make([]*Peer, 0, len(members))
	for _, member := range members {
		remaining = append(remaining, member)
	}
	m.mu.Unlock()

	message := SignalMessage{Type: "remove-peer", Source: peer.ID}
	for _, member := range remaining {
		member.enqueue(message)
	}
}

func (m *RoomManager) route(peer *Peer, message SignalMessage) {
	message.Source = peer.ID
	if message.Type == "chat" || message.Type == "presence" ||
		message.Type == "raise-hand" || message.Type == "presenting" {
		m.broadcast(peer.RoomID, message, peer.ID)
		return
	}
	if message.Target == "" {
		peer.enqueue(SignalMessage{Type: "error", Data: json.RawMessage(`{"message":"target peer is required"}`)})
		return
	}

	m.mu.Lock()
	members := m.rooms[peer.RoomID]
	var target *Peer
	if members != nil {
		target = members[message.Target]
	}
	m.mu.Unlock()
	if target == nil {
		peer.enqueue(SignalMessage{Type: "error", Data: json.RawMessage(`{"message":"target peer is not in this room"}`)})
		return
	}
	target.enqueue(message)
}

func (m *RoomManager) broadcast(roomID string, message SignalMessage, exceptID string) {
	m.mu.Lock()
	members := m.rooms[roomID]
	targets := make([]*Peer, 0, len(members))
	for id, member := range members {
		if id != exceptID {
			targets = append(targets, member)
		}
	}
	m.mu.Unlock()
	for _, target := range targets {
		target.enqueue(message)
	}
}

func (m *RoomManager) shutdownAll() {
	m.mu.Lock()
	peers := make([]*Peer, 0)
	for _, members := range m.rooms {
		for _, peer := range members {
			peers = append(peers, peer)
		}
	}
	m.mu.Unlock()
	for _, peer := range peers {
		peer.shutdown()
	}
}

func (p *Peer) enqueue(message SignalMessage) bool {
	select {
	case <-p.done:
		return false
	case p.out <- message:
		return true
	default:
		p.shutdown()
		return false
	}
}

func (p *Peer) shutdown() {
	p.once.Do(func() {
		close(p.done)
		_ = p.Conn.Close()
	})
}

func (p *Peer) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	defer p.shutdown()

	for {
		select {
		case <-p.done:
			return
		case message := <-p.out:
			_ = p.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := p.Conn.WriteJSON(message); err != nil {
				return
			}
		case <-ticker.C:
			if err := p.Conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
				return
			}
		}
	}
}

func (p *Peer) readPump() {
	defer p.shutdown()
	p.Conn.SetReadLimit(maxMessageBytes)
	_ = p.Conn.SetReadDeadline(time.Now().Add(pongWait))
	p.Conn.SetPongHandler(func(string) error {
		return p.Conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		var message SignalMessage
		if err := p.Conn.ReadJSON(&message); err != nil {
			return
		}
		_ = p.Conn.SetReadDeadline(time.Now().Add(pongWait))
		if err := validateSignalMessage(&message); err != nil {
			p.enqueue(SignalMessage{Type: "error", Data: marshalJSON(map[string]string{"message": err.Error()})})
			continue
		}
		roomManager.route(p, message)
	}
}

func validateSignalMessage(message *SignalMessage) error {
	switch message.Type {
	case "offer", "answer", "candidate":
		if message.Target == "" || len(message.Data) == 0 || !json.Valid(message.Data) {
			return errors.New("invalid peer signaling message")
		}
	case "chat":
		var data struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(message.Data, &data); err != nil {
			return errors.New("invalid chat message")
		}
		data.Text = strings.TrimSpace(data.Text)
		if data.Text == "" || utf8.RuneCountInString(data.Text) > maxChatRunes {
			return errors.New("chat message must be between 1 and 2000 characters")
		}
		message.Data = marshalJSON(struct {
			Text   string    `json:"text"`
			SentAt time.Time `json:"sentAt"`
		}{Text: data.Text, SentAt: time.Now().UTC()})
	case "presence":
		var data struct {
			Muted      bool `json:"muted"`
			VideoOff   bool `json:"videoOff"`
			Speaking   bool `json:"speaking"`
			RaisedHand bool `json:"raisedHand"`
			Presenting bool `json:"presenting"`
		}
		if err := json.Unmarshal(message.Data, &data); err != nil {
			return errors.New("invalid participant presence")
		}
		message.Data = marshalJSON(data)
	case "raise-hand", "presenting":
		var data struct {
			Active bool `json:"active"`
		}
		if err := json.Unmarshal(message.Data, &data); err != nil {
			return errors.New("invalid call status")
		}
		message.Data = marshalJSON(data)
	default:
		return errors.New("unsupported message type")
	}
	return nil
}

func marshalJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func handleSignaling(w http.ResponseWriter, r *http.Request) {
	roomID := r.URL.Query().Get("room")
	if !validRoomID(roomID) {
		http.Error(w, "valid room query parameter is required", http.StatusBadRequest)
		return
	}
	name, err := cleanDisplayName(r.URL.Query().Get("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	peer := &Peer{
		ID:     uuid.NewString(),
		Name:   name,
		RoomID: roomID,
		Conn:   conn,
		out:    make(chan SignalMessage, 64),
		done:   make(chan struct{}),
	}
	if err := roomManager.join(peer); err != nil {
		_ = conn.WriteJSON(SignalMessage{Type: "error", Data: marshalJSON(map[string]any{
			"message": err.Error(),
			"fatal":   true,
		})})
		_ = conn.Close()
		return
	}

	go peer.writePump()
	defer roomManager.leave(peer)
	peer.readPump()
}
