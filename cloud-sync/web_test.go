package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWeb_IndexServed(t *testing.T) {
	srv := httptest.NewServer(NewWebServer(nil, supervisorTestLogger()).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "cloud-sync") {
		t.Errorf("index body does not contain %q:\n%s", "cloud-sync", body)
	}
}

func TestWeb_UnknownAPI(t *testing.T) {
	srv := httptest.NewServer(NewWebServer(nil, supervisorTestLogger()).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/nope")
	if err != nil {
		t.Fatalf("GET /api/nope: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/nope status = %d, want 404", resp.StatusCode)
	}
}
