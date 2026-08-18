package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTokens writes the CLI tokens file in the test's configuration folder.
func writeTokens(t *testing.T, dir string, tokens map[string]any) {
	t.Helper()
	content, err := json.Marshal(tokens)
	if err != nil {
		t.Fatalf("marshal tokens: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, TokensFile), content, 0o600); err != nil {
		t.Fatalf("write %s: %v", TokensFile, err)
	}
}

func TestDecodeJWTExpiry(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour).Truncate(time.Second)
	if got := decodeJWTExpiry(fakeJWT(expiresAt)); !got.Equal(expiresAt) {
		t.Errorf("decodeJWTExpiry() = %v, want %v", got, expiresAt)
	}
}

func TestDecodeJWTExpiryWithoutUsableExpiry(t *testing.T) {
	claims := func(raw string) string {
		return "header." + base64.RawURLEncoding.EncodeToString([]byte(raw)) + ".signature"
	}
	for _, testCase := range []struct{ name, jwt string }{
		{"no exp claim", fakeJWT(time.Time{})},
		{"exp is not a number", claims(`{"exp":"soon"}`)},
		{"claims are not an object", claims(`["exp"]`)},
		{"claims are not JSON", claims(`not json`)},
		{"payload is not base64", "header.!!!.signature"},
		{"not a JWT", "opaque-token"},
		{"empty", ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := decodeJWTExpiry(testCase.jwt); !got.IsZero() {
				t.Errorf("decodeJWTExpiry() = %v, want the zero time", got)
			}
		})
	}
}

func TestNeedsRenew(t *testing.T) {
	now := time.Now()
	for _, testCase := range []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{"no expiration time is never renewed", time.Time{}, false},
		{"far from expiring", now.Add(time.Hour), false},
		{"just outside the skew", now.Add(renewSkew + time.Second), false},
		{"within the skew", now.Add(renewSkew - time.Second), true},
		{"already expired", now.Add(-time.Hour), true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			token := &authToken{expiresAt: testCase.expiresAt}
			if got := token.needsRenew(now); got != testCase.want {
				t.Errorf("needsRenew() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestLoginWithToken(t *testing.T) {
	server := startFakeServer(t)
	jwt := fakeJWT(time.Now().Add(time.Hour))
	server.acceptLoginToken("login-token", jwt)

	token, err := loginWithToken(
		context.Background(), http.DefaultClient, server.url, "login-token", sourceOptions)
	if err != nil {
		t.Fatalf("loginWithToken: %v", err)
	}
	if token.token != jwt {
		t.Errorf("token = %q, want %q", token.token, jwt)
	}
	if token.source != sourceOptions {
		t.Errorf("source = %q, want %q", token.source, sourceOptions)
	}
	if token.expiresAt.IsZero() {
		t.Error("expiresAt is the zero time, want it decoded from the JWT")
	}

	requests := server.recordedHTTP()
	if len(requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(requests))
	}
	if requests[0].path != "/auth/token/login-token?json" {
		t.Errorf("path = %q, want /auth/token/login-token?json", requests[0].path)
	}
	if requests[0].accept != "application/json" {
		t.Errorf("Accept = %q, want application/json", requests[0].accept)
	}
}

func TestLoginWithTokenRedactsTheTokenOnFailure(t *testing.T) {
	server := startFakeServer(t)

	_, err := loginWithToken(
		context.Background(), http.DefaultClient, server.url, "secret-token", sourceOptions)
	if err == nil {
		t.Fatal("loginWithToken succeeded, want an error")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("the error leaks the login token: %v", err)
	}
	if !strings.Contains(err.Error(), "/auth/token/***") {
		t.Errorf("the error does not name the redacted endpoint: %v", err)
	}
}

func TestLoginWithTokenFile(t *testing.T) {
	server := startFakeServer(t)
	jwt := fakeJWT(time.Now().Add(time.Hour))
	server.acceptLoginToken("login-token", jwt)

	file := filepath.Join(t.TempDir(), "token")
	// A trailing newline is what a file written by `echo` has.
	if err := os.WriteFile(file, []byte("login-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	token, err := loginWithTokenFile(
		context.Background(), http.DefaultClient, server.url, file, sourceEnvFile)
	if err != nil {
		t.Fatalf("loginWithTokenFile: %v", err)
	}
	if token.token != jwt {
		t.Errorf("token = %q, want %q", token.token, jwt)
	}
	if token.source != sourceEnvFile {
		t.Errorf("source = %q, want %q", token.source, sourceEnvFile)
	}
}

func TestLoginWithTokenFileErrors(t *testing.T) {
	server := startFakeServer(t)
	dir := t.TempDir()

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("  \n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	for _, testCase := range []struct{ name, file, want string }{
		{"missing", filepath.Join(dir, "missing"), "unable to read the login token"},
		{"empty", empty, "is empty"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := loginWithTokenFile(
				context.Background(), http.DefaultClient, server.url, testCase.file, sourceEnvFile)
			if err == nil {
				t.Fatal("loginWithTokenFile succeeded, want an error")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}
}

func TestRenewToken(t *testing.T) {
	server := startFakeServer(t)
	renewed := fakeJWT(time.Now().Add(2 * time.Hour))
	server.acceptRenewal(renewed)

	current := &authToken{token: "old-jwt", source: sourceCLI}
	token, err := renewToken(context.Background(), http.DefaultClient, server.url, current)
	if err != nil {
		t.Fatalf("renewToken: %v", err)
	}
	if token.token != renewed {
		t.Errorf("token = %q, want %q", token.token, renewed)
	}
	if token.source != sourceCLI {
		t.Errorf("source = %q, want the source of the original token %q", token.source, sourceCLI)
	}

	requests := server.recordedHTTP()
	if len(requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(requests))
	}
	if requests[0].path != "/auth/token?json" {
		t.Errorf("path = %q, want /auth/token?json", requests[0].path)
	}
	if requests[0].authorization != "Bearer old-jwt" {
		t.Errorf("Authorization = %q, want Bearer old-jwt", requests[0].authorization)
	}
}

func TestRenewTokenRejected(t *testing.T) {
	server := startFakeServer(t)
	current := &authToken{token: "old-jwt", source: sourceCLI}
	if _, err := renewToken(context.Background(), http.DefaultClient, server.url, current); err == nil {
		t.Fatal("renewToken succeeded, want an error")
	}
}

func TestLoginResponseWithoutToken(t *testing.T) {
	for _, testCase := range []struct{ name, payload string }{
		{"missing token", `{"other":"value"}`},
		{"empty token", `{"token":""}`},
		{"not an object", `["token"]`},
		{"not JSON", `not json`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := authTokenFromResponse([]byte(testCase.payload), sourceLogin); err == nil {
				t.Error("authTokenFromResponse succeeded, want an error")
			}
		})
	}
}

func TestCLIAuthToken(t *testing.T) {
	dir := configDir(t)
	jwt := fakeJWT(time.Now().Add(time.Hour))
	writeTokens(t, dir, map[string]any{
		"https://example.com": map[string]string{"Token": jwt},
	})

	token, err := cliAuthToken(dir, "https://example.com")
	if err != nil {
		t.Fatalf("cliAuthToken: %v", err)
	}
	if token == nil {
		t.Fatal("cliAuthToken returned no token, want the one from the tokens file")
	}
	if token.token != jwt {
		t.Errorf("token = %q, want %q", token.token, jwt)
	}
	if token.source != sourceCLI {
		t.Errorf("source = %q, want %q", token.source, sourceCLI)
	}
	if token.expiresAt.IsZero() {
		t.Error("expiresAt is the zero time, want it decoded from the JWT")
	}
}

func TestCLIAuthTokenMatchesTrailingSlash(t *testing.T) {
	dir := configDir(t)
	writeTokens(t, dir, map[string]any{
		"https://example.com/": map[string]string{"Token": "jwt"},
	})

	token, err := cliAuthToken(dir, "https://example.com")
	if err != nil {
		t.Fatalf("cliAuthToken: %v", err)
	}
	if token == nil || token.token != "jwt" {
		t.Errorf("cliAuthToken() = %v, want the entry saved with a trailing slash", token)
	}
}

func TestCLIAuthTokenAbsent(t *testing.T) {
	dir := configDir(t)

	t.Run("no tokens file", func(t *testing.T) {
		token, err := cliAuthToken(dir, "https://example.com")
		if err != nil || token != nil {
			t.Errorf("cliAuthToken() = %v, %v; want nil, nil", token, err)
		}
	})

	t.Run("no entry for the server", func(t *testing.T) {
		writeTokens(t, dir, map[string]any{
			"https://other.example.com": map[string]string{"Token": "jwt"},
		})
		token, err := cliAuthToken(dir, "https://example.com")
		if err != nil || token != nil {
			t.Errorf("cliAuthToken() = %v, %v; want nil, nil", token, err)
		}
	})

	t.Run("entry without a token", func(t *testing.T) {
		writeTokens(t, dir, map[string]any{
			"https://example.com": map[string]string{"Token": ""},
		})
		token, err := cliAuthToken(dir, "https://example.com")
		if err != nil || token != nil {
			t.Errorf("cliAuthToken() = %v, %v; want nil, nil", token, err)
		}
	})
}

func TestCLIAuthTokenUndecodable(t *testing.T) {
	dir := configDir(t)
	if err := os.WriteFile(filepath.Join(dir, TokensFile), []byte("not json"), 0o600); err != nil {
		t.Fatalf("write %s: %v", TokensFile, err)
	}

	if _, err := cliAuthToken(dir, "https://example.com"); err == nil {
		t.Error("cliAuthToken succeeded, want an error: the information is there but unusable")
	}
}

func TestRedact(t *testing.T) {
	for _, testCase := range []struct{ in, want string }{
		{"https://example.com/auth/token/secret?json", "https://example.com/auth/token/***?json"},
		{"https://example.com/auth/token?json", "https://example.com/auth/token?json"},
	} {
		if got := redact(testCase.in); got != testCase.want {
			t.Errorf("redact(%q) = %q, want %q", testCase.in, got, testCase.want)
		}
	}
}
