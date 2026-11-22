package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

func handleICEServers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	servers := []iceServer{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
	}
	turnURLs := splitCSV(os.Getenv("TURN_URLS"))
	if len(turnURLs) == 0 {
		turnURLs = splitCSV(os.Getenv("TURN_URL"))
	}
	if len(turnURLs) > 0 {
		if secret := strings.TrimSpace(os.Getenv("TURN_SHARED_SECRET")); secret != "" {
			ttl := turnCredentialTTL(os.Getenv("TURN_CREDENTIAL_TTL"))
			username := strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)
			mac := hmac.New(sha1.New, []byte(secret))
			_, _ = mac.Write([]byte(username))
			servers = append(servers, iceServer{
				URLs:       turnURLs,
				Username:   username,
				Credential: base64.StdEncoding.EncodeToString(mac.Sum(nil)),
			})
		} else if username := strings.TrimSpace(os.Getenv("TURN_USERNAME")); username != "" {
			if credential := strings.TrimSpace(os.Getenv("TURN_CREDENTIAL")); credential != "" {
				servers = append(servers, iceServer{
					URLs:       turnURLs,
					Username:   username,
					Credential: credential,
				})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		ICEServers []iceServer `json:"iceServers"`
	}{ICEServers: servers})
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func turnCredentialTTL(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds < 60 || seconds > 86400 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}
