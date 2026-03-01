package main

import (
	"flag"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address (host:port)")
	flag.Parse()

	http.HandleFunc("/", serveIndex)
	http.HandleFunc("/ws", handleSignaling)
	http.Handle("/css/", http.StripPrefix("/css/", http.FileServer(http.Dir("./public/css"))))
	http.Handle("/js/", http.StripPrefix("/js/", http.FileServer(http.Dir("./public/js"))))

	log.Printf("webrtcV1 server listening on http://localhost%s", *addr)
	log.Fatal(http.ListenAndServe(":8080","server.crt", "server.key", nil))
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, "public/index.html")
}
