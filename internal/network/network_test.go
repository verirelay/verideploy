package network

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "body") }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/signed":
			http.Redirect(w, r, target.URL+"/asset?sig=abc", http.StatusFound)
		case "/insecure":
			http.Redirect(w, r, "http://example.com/asset", http.StatusFound)
		}
	}))
	defer source.Close()
	if body, err := Get(t.Context(), Client(true), source.URL+"/signed", 16); err != nil || string(body) != "body" {
		t.Fatalf("signed redirect: %q %v", body, err)
	}
	if _, err := Get(t.Context(), Client(true), source.URL+"/insecure", 16); err == nil {
		t.Fatal("followed a redirect to plain HTTP")
	}
	if _, err := Get(t.Context(), Client(false), source.URL+"/signed", 16); err == nil {
		t.Fatal("followed a redirect with follow off")
	}
	if _, err := Get(t.Context(), Client(true), source.URL+"/signed", 2); err == nil {
		t.Fatal("accepted a body over the limit")
	}
}

func TestCheckURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://registry.verirelay.com": true,
		"http://127.0.0.1:8080":          true,
		"http://registry.verirelay.com":  false,
		"https://user@host":              false,
		"https://host/?q=1":              false,
		"registry.verirelay.com":         false,
	} {
		if err := CheckURL(raw); (err == nil) != ok {
			t.Errorf("CheckURL(%q) = %v", raw, err)
		}
	}
}
