package handlers

import (
	"net/http"
	"os"

	goauth "github.com/construct-space/go-auth"
)

// GatewayIdentity is the decoded identity a trusted gateway has asserted on
// the request. Re-export the shared shape so developer and graph speak the
// same contract for X-Internal-Secret + X-Auth-* headers.
type GatewayIdentity = goauth.Identity

// gatewayIdentity returns the identity the gateway has asserted, or nil if
// the request didn't come through the gateway (no X-Internal-Secret, secret
// mismatch, or no X-Auth-User-ID set). Safe to call on every request — it's
// cheap and side-effect free.
//
// The gateway is the ONLY thing that should ever be able to send
// X-Internal-Secret; INTERNAL_SHARED_SECRET lives only in gateway + service
// env vars (never on the public internet, never in client code). When the
// secret matches, the service treats the X-Auth-* headers as authoritative.
func gatewayIdentity(r *http.Request) *GatewayIdentity {
	id := goauth.Gateway(r, os.Getenv("INTERNAL_SHARED_SECRET"))
	if !id.Authenticated() {
		return nil
	}
	return &id
}

// currentUserID resolves the effective user UUID for the request from any
// auth source the service accepts. Returns empty string when nothing is
// authenticated. Prefer this over calling getSession / getCLIToken directly
// in handlers that only need a user ID — it keeps the auth-source ordering
// in one place.
//
// Order: gateway identity → web session → CLI / OAuth / publisher token.
func currentUserID(r *http.Request) string {
	if id := gatewayIdentity(r); id != nil {
		return id.UserID
	}
	if s := getSession(r); s != nil && s.UserID != nil {
		return *s.UserID
	}
	if t := getCLIToken(r); t != nil {
		return t.UserID
	}
	return ""
}
