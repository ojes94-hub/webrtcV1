package main

import (
	"bytes"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed public
var publicFiles embed.FS

func main() {
	defaultAddr := ":8080"
	if port := strings.TrimSpace(os.Getenv("PORT")); port != "" {
		defaultAddr = ":" + strings.TrimPrefix(port, ":")
	}

	addr := flag.String("addr", defaultAddr, "listen address (host:port)")
	flag.Parse()

	publicFS, err := fs.Sub(publicFiles, "public")
	if err != nil {
		log.Fatalf("prepare public assets: %v", err)
	}
	fileServer := http.FileServer(http.FS(publicFS))

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveIndex)
	mux.HandleFunc("/healthz", serveHealth)
	mux.HandleFunc("/ws", handleSignaling)
	mux.Handle("/css/", fileServer)
	mux.Handle("/js/", fileServer)
	mux.Handle("/images/", fileServer)

	log.Printf("webrtcV1 server listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := publicFiles.ReadFile("public/index.html")
	if err != nil {
		http.Error(w, "index unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(data))
}

func serveHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
