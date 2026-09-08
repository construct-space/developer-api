package middleware

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"construct/dev-portal/internal/config"
)

func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline' https://code.iconify.design; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self' https://api.iconify.design https://api.simplesvg.com https://api.unisvg.com; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func CORS(cfg *config.Config) func(http.Handler) http.Handler {
	// Public API paths allow any origin (consumed by Tauri desktop app, CLI, etc.)
	publicPaths := []string{"/api/registry", "/api/downloads/", "/api/spaces", "/api/categories"}

	// Paths the Construct desktop app hits with credentials (cookies or
	// Bearer). These need explicit origin echo + Allow-Credentials. The
	// app origin is unpredictable at build time (Tauri webview scheme +
	// localhost dev server + the portal itself), so we pattern-match.
	credentialedPaths := []string{"/api/auth/", "/api/enroll/", "/api/publisher", "/api/publishers/", "/api/spaces/", "/api/data/", "/api/transfers"}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			path := r.URL.Path

			// Allow any origin for public, read-only API endpoints.
			isPublic := false
			for _, p := range publicPaths {
				if path == p || strings.HasPrefix(path, p) {
					isPublic = true
					break
				}
			}

			switch {
			case isPublic && origin != "":
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

			case origin != "" && (isAppOrigin(origin) || originInList(origin, cfg.AllowedOrigins)):
				// Credentialed API calls from the desktop app (any localhost
				// port, Tauri webview, or an origin explicitly allowlisted
				// via CORS_ORIGINS). Echo the origin — wildcards + creds are
				// forbidden by the spec.
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Space-ID, X-Project-ID, X-API-Key")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				// Cache preflight for 10 min — CLIs poll /api/auth/cli-verify.
				if isCredentialedPath(path, credentialedPaths) {
					w.Header().Set("Access-Control-Max-Age", "600")
				}
			}

			if r.Method == "OPTIONS" {
				w.WriteHeader(204)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isAppOrigin reports whether the origin belongs to a first-party
// Construct surface — desktop app webview, local dev server, or the
// lisaos.dev family. Cheap + safe: we only echo it, never trust it
// for auth decisions (the real auth is session cookies + Bearer tokens).
func isAppOrigin(origin string) bool {
	// Tauri webview
	if strings.HasPrefix(origin, "tauri://") ||
		strings.HasPrefix(origin, "https://tauri.localhost") ||
		strings.HasPrefix(origin, "http://tauri.localhost") {
		return true
	}
	// Local dev servers on any port
	if strings.HasPrefix(origin, "http://localhost:") ||
		strings.HasPrefix(origin, "http://127.0.0.1:") ||
		strings.HasPrefix(origin, "https://localhost:") {
		return true
	}
	// Construct family. Suffix match so subdomains (accounts, developer,
	// spaces, graph, app) all qualify — useful when the portal wants to
	// call the dev portal from the accounts site and vice versa.
	if strings.HasSuffix(origin, ".lisaos.dev") ||
		origin == "https://lisaos.dev" {
		return true
	}
	return false
}

func originInList(origin string, allowed []string) bool {
	for _, o := range allowed {
		if o == origin || o == "*" {
			return true
		}
	}
	return false
}

func isCredentialedPath(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func RateLimit(next http.Handler) http.Handler {
	type bucket struct {
		count   int
		resetAt time.Time
	}
	buckets := map[string]*bucket{}
	var bucketsMu sync.Mutex

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !strings.HasPrefix(path, "/api/auth/") && path != "/api/register" {
			next.ServeHTTP(w, r)
			return
		}

		ip := r.RemoteAddr
		if idx := strings.LastIndex(ip, ":"); idx > 0 {
			ip = ip[:idx]
		}

		now := time.Now()
		bucketsMu.Lock()
		b, ok := buckets[ip]
		if !ok || now.After(b.resetAt) {
			buckets[ip] = &bucket{count: 1, resetAt: now.Add(time.Minute)}
		} else {
			b.count++
			if b.count > 30 {
				bucketsMu.Unlock()
				http.Error(w, "Too many requests", http.StatusTooManyRequests)
				return
			}
		}
		for key, bucket := range buckets {
			if now.After(bucket.resetAt) {
				delete(buckets, key)
			}
		}
		bucketsMu.Unlock()

		next.ServeHTTP(w, r)
	})
}
