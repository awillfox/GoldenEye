package auth

import (
	"context"
	"net/http"
)

type contextKey string

const claimsKey contextKey = "claims"

// Middleware returns a chi-compatible middleware that validates the session cookie.
// Unauthenticated requests redirect to /login.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieName)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		claims, err := s.VerifyToken(cookie.Value)
		if err != nil {
			// Clear bad cookie and redirect.
			http.SetCookie(w, &http.Cookie{Name: cookieName, MaxAge: -1, Path: "/"})
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		ctx := context.WithValue(r.Context(), claimsKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AdminOnly is middleware that allows only users with role=admin.
// Must be placed after Middleware in the chain.
func AdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := ClaimsFromCtx(r.Context())
		if claims == nil || claims.Role != RoleAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ClaimsFromCtx retrieves auth claims stored by Middleware.
// Returns nil if not present (e.g., public routes).
func ClaimsFromCtx(ctx context.Context) *Claims {
	c, _ := ctx.Value(claimsKey).(*Claims)
	return c
}
