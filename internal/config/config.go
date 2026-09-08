package config

import (
	"bufio"
	"os"
	"strings"
)

func init() {
	loadEnvFile(".env")
}

func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, "="); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			if os.Getenv(key) == "" {
				os.Setenv(key, val)
			}
		}
	}
}

type Config struct {
	Port            string
	DBDriver        string
	DBHost          string
	DBPort          string
	DBUser          string
	DBPass          string
	DBName          string
	DBSSL           string
	AppURL          string
	OAuthAuthorize  string
	OAuthToken      string
	OAuthUserInfo   string
	OAuthClientID   string
	OAuthSecret      string
	OAuthRedirectURI string
	OAuthScope       string
	OAuthCLIClientID string
	OAuthCLISecret   string
	AllowedOrigins  []string
	PaasURL         string
	// InternalSecret is a shared secret for service-to-service calls
	// (e.g. accounts /me enrichment hitting /internal/* endpoints here).
	InternalSecret  string
	// SourceURL is the base URL for the source service, used to verify org
	// ownership during enroll and to seed/unseed the Developer role.
	SourceURL       string
	// AccountsURL is the base URL for the accounts service. Used for
	// internal user-info lookups (e.g. resolving publisher_user_id →
	// display name in publish-history responses).
	AccountsURL     string
	// MarketplaceURL is the base URL for marketplace-api. Approved spaces
	// are pushed there on approve; unpublish triggers a soft-demote.
	// In CapRover this is the swarm overlay hostname so we skip the
	// public gateway hop. Public domain works too if INTERNAL_SHARED_SECRET
	// matches.
	MarketplaceURL  string
	// SpaceSigningKeyPEM is the PKCS8 PEM of the P-256 private key used to sign
	// published space bundles (app.iife.js). Empty -> bundles ship unsigned.
	SpaceSigningKeyPEM string
	// SpaceSigningKeyID identifies the signing key; matches a pinned id in the
	// host's TRUSTED_SPACE_KEYS.
	SpaceSigningKeyID  string
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func Load() *Config {
	return &Config{
		Port:            env("PORT", "4100"),
		DBDriver:        env("DB_DRIVER", "mysql"),
		DBHost:          env("DB_HOST", "localhost"),
		DBPort:          env("DB_PORT", "3306"),
		DBUser:          env("DB_USER", "root"),
		DBPass:          env("DB_PASS", ""),
		DBName:          env("DB_NAME", "construct"),
		DBSSL:           env("DB_SSL", "false"),
		AppURL:          env("APP_URL", "http://localhost:4100"),
		// OAuth browser redirects go through the public gateway (my) because
		// accounts.lisaos.dev no longer resolves externally. OAuthUserInfo
		// is server-to-server — prefer the internal CapRover hostname so we
		// skip the extra TLS hop and the gateway's session gate. Both env vars
		// still override per environment.
		OAuthAuthorize:  env("OAUTH_AUTHORIZE_URL", "https://my.lisaos.dev/api/accounts/oauth/authorize"),
		OAuthToken:      env("OAUTH_TOKEN_URL", "https://my.lisaos.dev/api/accounts/oauth/token"),
		OAuthUserInfo:   env("OAUTH_USERINFO_URL", "http://srv-captain--accounts/api/me"),
		OAuthClientID:   env("OAUTH_CLIENT_ID", ""),
		OAuthSecret:     env("OAUTH_CLIENT_SECRET", ""),
		OAuthRedirectURI: env("OAUTH_REDIRECT_URI", "http://localhost:4100/api/auth/callback"),
		OAuthScope:       env("OAUTH_SCOPE", "profile email"),
		OAuthCLIClientID: env("OAUTH_CLI_CLIENT_ID", ""),
		OAuthCLISecret:   env("OAUTH_CLI_CLIENT_SECRET", ""),
		PaasURL:         env("PAAS_URL", "https://paas.construct.ninja"),
		InternalSecret:  env("INTERNAL_SHARED_SECRET", ""),
		SourceURL:       env("SOURCE_URL", "https://source.lisaos.dev"),
		// In CapRover the internal hostname avoids the gateway hop. Public
		// accounts.lisaos.dev no longer resolves externally.
		AccountsURL:     env("ACCOUNTS_URL", "http://srv-captain--accounts"),
		MarketplaceURL:  env("MARKETPLACE_URL", "http://srv-captain--marketplace-api"),
		AllowedOrigins:  append(
			[]string{env("APP_URL", "http://localhost:4100"), "tauri://localhost", "https://tauri.localhost"},
			splitCSV(env("CORS_ORIGINS", ""))...,
		),
		SpaceSigningKeyPEM: env("SPACE_SIGNING_KEY_PEM", ""),
		SpaceSigningKeyID:  env("SPACE_SIGNING_KEY_ID", "marketplace-2026-05"),
	}
}
