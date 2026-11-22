package main

import (
	"bytes"
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
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
	certFile := flag.String("cert", "", "TLS certificate file (optional)")
	keyFile := flag.String("key", "", "TLS private key file (optional)")
	flag.Parse()
	if (*certFile == "") != (*keyFile == "") {
		log.Fatal("both -cert and -key must be provided to enable TLS")
	}

	handler, err := newHandler()
	if err != nil {
		log.Fatalf("prepare HTTP handler: %v", err)
	}

	log.Printf("webrtcV1 server listening on %s", *addr)
	server := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serverErrors := make(chan error, 1)
	go func() {
		if *certFile != "" {
			serverErrors <- server.ListenAndServeTLS(*certFile, *keyFile)
			return
		}
		serverErrors <- server.ListenAndServe()
	}()

	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serverErrors:
		if err != http.ErrServerClosed {
			log.Fatal(err)
		}
	case <-shutdown.Done():
		roomManager.shutdownAll()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("HTTP server shutdown error: %v", err)
			_ = server.Close()
		}
		if err := <-serverErrors; err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error during shutdown: %v", err)
		}
	}
}

func newHandler() (http.Handler, error) {
	publicFS, err := fs.Sub(publicFiles, "public")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(publicFS))
	mux := http.NewServeMux()
	mux.HandleFunc("/", serveIndex)
	mux.HandleFunc("/room/", serveIndex)
	mux.HandleFunc("/healthz", serveHealth)
	mux.HandleFunc("/ws", handleSignaling)
	mux.HandleFunc("/api/ice-servers", handleICEServers)
	mux.HandleFunc("/api/conference-token", handleConferenceToken)
	mux.Handle("/css/", fileServer)
	mux.Handle("/js/", fileServer)
	mux.Handle("/images/", fileServer)
	return mux, nil
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && !isRoomPath(r.URL.Path) {
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

func isRoomPath(path string) bool {
	if !strings.HasPrefix(path, "/room/") {
		return false
	}
	return validRoomID(strings.TrimPrefix(path, "/room/"))
}

func serveHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
