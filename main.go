package main

import (
	"flag"
	"log"
	"net/http"
	"os"
)

func main() {
	defaultPort := os.Getenv("PORT")
	if defaultPort == "" {
		defaultPort = "8080"
	}

	addr := flag.String("addr", ":"+defaultPort, "listen address (host:port)")
	certFile := flag.String("cert", "", "optional TLS certificate file")
	keyFile := flag.String("key", "", "optional TLS key file")
	flag.Parse()

	http.HandleFunc("/", serveIndex)
	http.HandleFunc("/ws", handleSignaling)
	http.Handle("/css/", http.StripPrefix("/css/", http.FileServer(http.Dir("./public/css"))))
	http.Handle("/js/", http.StripPrefix("/js/", http.FileServer(http.Dir("./public/js"))))

	if *certFile != "" && *keyFile != "" {
		log.Printf("webrtcV1 server listening with TLS on %s", *addr)
		log.Fatal(http.ListenAndServeTLS(*addr, *certFile, *keyFile, nil))
	} else {
		log.Printf("webrtcV1 server listening on %s", *addr)
		log.Fatal(http.ListenAndServe(*addr, nil))
	}
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, "public/index.html")
}
