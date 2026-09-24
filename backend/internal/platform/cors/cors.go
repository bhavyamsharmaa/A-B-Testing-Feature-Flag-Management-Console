// Package cors lets the browser console (served from a different origin,
// e.g. Vercel) call this API with an Authorization header.
package cors

import (
	"net/http"
	"slices"
)

// Middleware allows exactly the listed origins. An empty list sends no CORS
// headers at all, so cross-origin browser calls are refused by default.
// Preflight OPTIONS requests are answered here, before routing, because the
// method-specific routes would otherwise reply 405.
func Middleware(allowedOrigins []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowed := origin != "" && slices.Contains(allowedOrigins, origin)
		if allowed {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Helios-SDK-Key")
			h.Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if allowed {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusForbidden)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}
