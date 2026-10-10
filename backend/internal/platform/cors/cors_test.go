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
