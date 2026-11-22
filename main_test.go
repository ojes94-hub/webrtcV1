package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
