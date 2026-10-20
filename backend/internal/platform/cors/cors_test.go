package cors

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAllowedOriginSeesRetryAfterAndOthersGetNothing(t *testing.T) {
	h := Middleware([]string{"https://console.example"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	}))

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Origin", "https://console.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "Retry-After") {
		t.Errorf("Retry-After is not exposed to the browser: %q", got)
	}
	if got := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "Content-Disposition") { // the console reads the file name of an export from it
		t.Errorf("Content-Disposition is not exposed to the browser: %q", got)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://console.example" {
		t.Error("the allowed origin is not echoed")
	}

	req = httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Access-Control-Expose-Headers") != "" {
		t.Errorf("another origin got CORS headers: %v", rec.Header())
	}
}

// Machine routes are never offered to browsers: no CORS headers, and a preflight is not answered.
func TestInternalPathsGetNoCORS(t *testing.T) {
	var reached bool
	h := Middleware([]string{"https://console.example.com"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	req := httptest.NewRequest("POST", "/internal/scheduler/run", nil)
	req.Header.Set("Origin", "https://console.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !reached || rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Vary") != "" {
		t.Errorf("an allowed origin got CORS on an internal route: %v", rec.Header())
	}
	pre := httptest.NewRequest("OPTIONS", "/internal/scheduler/run", nil)
	pre.Header.Set("Origin", "https://console.example.com")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	reached = false
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, pre)
	if !reached || rec.Code == http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Methods") != "" {
		t.Errorf("a preflight to an internal route was answered: %d %v", rec.Code, rec.Header())
	}
	// ordinary routes are unchanged
	rec = httptest.NewRecorder()
	ok := httptest.NewRequest("GET", "/flags", nil)
	ok.Header.Set("Origin", "https://console.example.com")
	h.ServeHTTP(rec, ok)
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://console.example.com" {
		t.Error("the console lost its CORS headers")
	}
}
