package auth

import (
	"context"
	"strings"
)

type claimsContextKey struct{}

// ContextWithClaims attaches verified claims to ctx.
func ContextWithClaims(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsContextKey{}, claims)
}

// ClaimsFromContext returns the verified claims attached to ctx, if any.
func ClaimsFromContext(ctx context.Context) *Claims {
	claims, _ := ctx.Value(claimsContextKey{}).(*Claims)
	return claims
}

// ClaimsValidator is the token-validation seam used by the HTTP security
// handlers. *Authenticator satisfies it.
type ClaimsValidator interface {
	ValidateClaims(token string) (*Claims, error)
}

// ClaimsFromBearerToken is the single token-validation path shared by the
// generated users/support security handlers and requireAuth.
func ClaimsFromBearerToken(validator ClaimsValidator, token string) (*Claims, error) {
	if validator == nil {
		return nil, ErrInvalidToken
	}
	return validator.ValidateClaims(token)
}

// ClaimsFromBearerHeader extracts and validates a Bearer token from an HTTP
// Authorization header value.
func ClaimsFromBearerHeader(validator ClaimsValidator, header string) (*Claims, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return nil, ErrInvalidToken
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	return ClaimsFromBearerToken(validator, token)
}
