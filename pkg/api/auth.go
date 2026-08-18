package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// EnvAuthToken is the environment variable carrying a login token for
	// service account login.
	EnvAuthToken = "CRAFTING_SANDBOX_AUTH_TOKEN"

	// EnvAuthTokenFile is the environment variable carrying the path of a file
	// whose content is a login token for service account login.
	EnvAuthTokenFile = "CRAFTING_SANDBOX_AUTH_TOKEN_FILE"

	// TokensFile is the name of the file inside the configuration folder where
	// the CLI saves the JWT tokens it obtained, keyed by server URL.
	TokensFile = "tokens"

	// renewSkew is how long before the actual expiration time a token is
	// treated as expired, so it gets renewed before a call fails.
	renewSkew = time.Minute
)

// authTokenSource tells where an authentication token came from.
type authTokenSource string

const (
	// sourceOptions is exchanged from a login token passed in the connector options.
	sourceOptions authTokenSource = "options"

	// sourceEnv is exchanged from a login token in EnvAuthToken.
	sourceEnv authTokenSource = "env"

	// sourceEnvFile is exchanged from a login token in the file named by EnvAuthTokenFile.
	sourceEnvFile authTokenSource = "env-file"

	// sourceCLI is read from the CLI authentication context.
	sourceCLI authTokenSource = "cli"

	// sourceLogin is obtained by an explicit call to Connector.Login.
	sourceLogin authTokenSource = "login"
)

// authToken is an authentication token used as gRPC metadata authorization.
type authToken struct {
	// token is the signed JWT token.
	token string

	// expiresAt is the expiration time of the token, as decoded from the JWT
	// exp claim. It is the zero time when the token carries no expiration
	// time, in which case it is never renewed.
	expiresAt time.Time

	// source is where the token came from.
	source authTokenSource
}

// needsRenew reports whether the token is expired or about to expire at the
// given time, and so has to be renewed before it is used.
func (t *authToken) needsRenew(now time.Time) bool {
	return !t.expiresAt.IsZero() && !now.Before(t.expiresAt.Add(-renewSkew))
}

// decodeJWTExpiry decodes the expiration time from a JWT token.
//
// The signature is not verified: only the server can do that. The expiration
// time is used locally to decide when to renew the token. It returns the zero
// time when the token has no usable expiration time.
func decodeJWTExpiry(jwt string) time.Time {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return time.Time{}
	}
	// A JWT is base64url without padding, but tolerate the padded form.
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp *float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}
	}
	if claims.Exp == nil || math.IsNaN(*claims.Exp) || math.IsInf(*claims.Exp, 0) {
		return time.Time{}
	}
	seconds, fraction := math.Modf(*claims.Exp)
	return time.Unix(int64(seconds), int64(fraction*float64(time.Second)))
}

// loginWithToken performs the service account login procedure with a login
// token. The source is recorded on the result.
func loginWithToken(
	ctx context.Context,
	client *http.Client,
	serverURL string,
	loginToken string,
	source authTokenSource,
) (*authToken, error) {
	endpoint := fmt.Sprintf("%s/auth/token/%s?json", serverURL, url.PathEscape(loginToken))
	payload, err := fetchJSON(ctx, client, endpoint, nil)
	if err != nil {
		return nil, err
	}
	return authTokenFromResponse(payload, source)
}

// loginWithTokenFile performs the service account login procedure with a login
// token stored in a file. The source is recorded on the result.
func loginWithTokenFile(
	ctx context.Context,
	client *http.Client,
	serverURL string,
	file string,
	source authTokenSource,
) (*authToken, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("unable to read the login token from %s: %w", file, err)
	}
	loginToken := strings.TrimSpace(string(content))
	if loginToken == "" {
		return nil, fmt.Errorf("the login token file %s is empty", file)
	}
	return loginWithToken(ctx, client, serverURL, loginToken, source)
}

// renewToken renews an authentication token before it expires, keeping the
// source of the original token.
func renewToken(
	ctx context.Context,
	client *http.Client,
	serverURL string,
	current *authToken,
) (*authToken, error) {
	payload, err := fetchJSON(ctx, client, serverURL+"/auth/token?json", map[string]string{
		"Authorization": "Bearer " + current.token,
	})
	if err != nil {
		return nil, err
	}
	return authTokenFromResponse(payload, current.source)
}

// cliAuthToken retrieves the JWT token saved by the CLI for the target server.
//
// A missing tokens file, or a file without an entry for the target server,
// simply means no CLI authentication context is available, and is reported as a
// nil token without an error. A tokens file which cannot be decoded is reported
// as an error, because the information is there but unusable.
func cliAuthToken(configFolder string, serverURL string) (*authToken, error) {
	tokensFile := filepath.Join(configFolder, TokensFile)
	content, err := os.ReadFile(tokensFile)
	if err != nil {
		return nil, nil
	}

	var tokens map[string]struct {
		Token string `json:"Token"`
	}
	if err := json.Unmarshal(content, &tokens); err != nil {
		return nil, fmt.Errorf("unable to decode %s: %w", tokensFile, err)
	}

	// The CLI may or may not have saved the URL with a trailing slash.
	entry, ok := tokens[serverURL]
	if !ok {
		entry = tokens[serverURL+"/"]
	}
	if entry.Token == "" {
		return nil, nil
	}
	return &authToken{
		token:     entry.Token,
		expiresAt: decodeJWTExpiry(entry.Token),
		source:    sourceCLI,
	}, nil
}

// authTokenFromResponse builds an authToken from a login or renew response
// payload, recording where the original login token came from.
func authTokenFromResponse(payload []byte, source authTokenSource) (*authToken, error) {
	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("unexpected login response: %w", err)
	}
	if response.Token == "" {
		return nil, fmt.Errorf(`unexpected login response: missing field "token"`)
	}
	return &authToken{
		token:     response.Token,
		expiresAt: decodeJWTExpiry(response.Token),
		source:    source,
	}, nil
}

// fetchJSON sends an HTTP GET expecting a JSON response, and returns the
// undecoded response body.
func fetchJSON(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	headers map[string]string,
) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to request %s: %w", redact(endpoint), err)
	}
	request.Header.Set("Accept", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("unable to reach %s: %w", redact(endpoint), err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s failed: %s", redact(endpoint), response.Status)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("unable to read the response of %s: %w", redact(endpoint), err)
	}
	return body, nil
}

// loginTokenInURL matches the login token in the path of a login URL.
var loginTokenInURL = regexp.MustCompile(`(/auth/token/)[^?/]+`)

// redact removes the login token from a URL, so it never shows up in an error
// message.
func redact(endpoint string) string {
	return loginTokenInURL.ReplaceAllString(endpoint, "${1}***")
}
