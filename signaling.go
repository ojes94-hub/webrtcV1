package main

import (
	"log"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Peer struct {
	ID   string
	Conn *websocket.Conn
	mu   sync.Mutex
}

var (
	peers      = make(map[string]*Peer)
	peersMutex sync.RWMutex
)

type Message struct {
	Type   string                 `json:"type"`
	Source string                 `json:"source,omitempty"`
	Target string                 `json:"target,omitempty"`
	Data   map[string]interface{} `json:"data,omitempty"`
}

func handleSignaling(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	id := uuid.NewString()
	peer := &Peer{ID: id, Conn: conn}

	peersMutex.Lock()
	peers[id] = peer
	existing := make([]string, 0, len(peers)-1)
	for pid := range peers {
		if pid != id {
			existing = append(existing, pid)
		}
	}
	peersMutex.Unlock()

	// send welcome including our id and current peers
	peer.send(Message{Type: "welcome", Data: map[string]interface{}{
		"id":    id,
		"peers": existing,
	}})

	// notify others about new peer
	broadcast(Message{Type: "new-peer", Source: id}, id)

	defer func() {
		peersMutex.Lock()
		delete(peers, id)
		peersMutex.Unlock()
		broadcast(Message{Type: "remove-peer", Source: id}, id)
	}()

	for {
		var msg Message
		if err := conn.ReadJSON(&msg); err != nil {
			log.Printf("read error: %v", err)
			return
		}
		msg.Source = id
		if msg.Target != "" {
			sendTo(msg.Target, msg)
		} else {
			broadcast(msg, id)
		}
	}
}

func (p *Peer) send(msg Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Conn.WriteJSON(msg)
}

func broadcast(msg Message, exceptIDs ...string) {
	peersMutex.RLock()
	defer peersMutex.RUnlock()
	skip := map[string]bool{}
	for _, e := range exceptIDs {
		skip[e] = true
	}
	for id, p := range peers {
		if skip[id] {
			continue
		}
		if err := p.send(msg); err != nil {
			log.Printf("broadcast error to %s: %v", id, err)
		}
	}
}

func sendTo(target string, msg Message) {
	peersMutex.RLock()
	defer peersMutex.RUnlock()
	if p, ok := peers[target]; ok {
		if err := p.send(msg); err != nil {
			log.Printf("error sending to %s: %v", target, err)
		}
	}
}
