package api

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// EnvConfigDir is the environment variable overriding the local
	// configuration folder.
	EnvConfigDir = "SANDBOX_CONFIG_DIR"

	// EnvServerURL is the environment variable overriding the target server URL.
	EnvServerURL = "CRAFTING_SANDBOX_SERVER_URL"

	// DefaultServerURL is the server URL used when nothing else specifies one.
	DefaultServerURL = "https://sandboxes.cloud"

	// ConfigFile is the name of the config file inside the configuration folder.
	ConfigFile = "config.toml"
)

// configuration is the resolved configuration of a Connector.
type configuration struct {
	// configFolder is the local configuration folder, where the config file
	// and the CLI authentication context are stored.
	configFolder string

	// serverURL is the URL of the target server, without a trailing slash.
	serverURL string

	// grpcTarget is the gRPC target of the server, as host:port.
	grpcTarget string

	// secure reports whether the server URL uses TLS, which decides both the
	// gRPC transport credentials and whether the authentication token may be
	// sent at all.
	secure bool
}

// defaultConfigFolder returns the default configuration folder for the current
// platform:
//
//   - on Mac, ~/.crafting/sandbox;
//   - on Linux (including WSL) and everything else, ~/.config/crafting/sandbox.
func defaultConfigFolder() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Without a home folder there is no default location. Return a
		// relative path rather than failing: a missing configuration folder
		// is not an error, it only means there is nothing to read from it.
		home = ""
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, ".crafting", "sandbox")
	}
	return filepath.Join(home, ".config", "crafting", "sandbox")
}

// resolveConfigFolder resolves the configuration folder, which is taken from
// EnvConfigDir when set and falls back to defaultConfigFolder.
func resolveConfigFolder() string {
	if fromEnv := os.Getenv(EnvConfigDir); fromEnv != "" {
		return fromEnv
	}
	return defaultConfigFolder()
}

// resolveConfiguration resolves the configuration.
//
// The server URL is resolved in the following order:
//
//   - EnvServerURL;
//   - serverURLOverride, from the connector options;
//   - server_url from $configFolder/config.toml;
//   - DefaultServerURL.
//
// It fails when the resolved server URL is not a usable http or https URL.
func resolveConfiguration(serverURLOverride string) (configuration, error) {
	configFolder := resolveConfigFolder()

	serverURL := os.Getenv(EnvServerURL)
	if serverURL == "" {
		serverURL = serverURLOverride
	}
	if serverURL == "" {
		serverURL = serverURLFromConfigFile(configFolder)
	}
	if serverURL == "" {
		serverURL = DefaultServerURL
	}

	normalized, parsed, err := normalizeServerURL(serverURL)
	if err != nil {
		return configuration{}, err
	}
	return configuration{
		configFolder: configFolder,
		serverURL:    normalized,
		grpcTarget:   grpcTarget(parsed),
		secure:       parsed.Scheme == "https",
	}, nil
}

// serverURLFromConfigFile reads server_url from the config file in the
// configuration folder.
//
// A missing or unreadable config file is not an error: the caller falls through
// to the next source of the server URL.
func serverURLFromConfigFile(configFolder string) string {
	content, err := os.ReadFile(filepath.Join(configFolder, ConfigFile))
	if err != nil {
		return ""
	}
	return parseTOMLStrings(string(content))["server_url"]
}

// normalizeServerURL strips the trailing slashes from a server URL so it can be
// concatenated with an absolute path, and validates it is a usable URL at all.
func normalizeServerURL(rawURL string) (string, *url.URL, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", nil, fmt.Errorf("invalid server URL %q: %w", rawURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", nil, fmt.Errorf("unsupported scheme in server URL %q", rawURL)
	}
	if parsed.Hostname() == "" {
		return "", nil, fmt.Errorf("missing host in server URL %q", rawURL)
	}
	return trimmed, parsed, nil
}

// grpcTarget returns the gRPC target of a server URL, as host:port, defaulting
// the port to the one implied by the scheme.
func grpcTarget(parsed *url.URL) string {
	port := parsed.Port()
	if port == "" {
		port = "80"
		if parsed.Scheme == "https" {
			port = "443"
		}
	}
	return net.JoinHostPort(parsed.Hostname(), port)
}

// parseTOMLStrings extracts the top-level string keys from a TOML document.
//
// The configuration file is only consulted for a handful of flat string values,
// so this deliberately implements the small subset of TOML needed for that:
// top-level `key = "value"` pairs, comments, and basic/literal strings. Keys
// inside a [table] are ignored, as are non-string values.
func parseTOMLStrings(content string) map[string]string {
	values := make(map[string]string)
	inTable := false
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			// Any table or array-of-tables header ends the top-level section.
			inTable = true
			continue
		}
		if inTable {
			continue
		}
		separator := strings.Index(line, "=")
		if separator < 0 {
			continue
		}
		key := strings.Trim(strings.TrimSpace(line[:separator]), `"'`)
		value, ok := parseTOMLString(strings.TrimSpace(line[separator+1:]))
		if key != "" && ok {
			values[key] = value
		}
	}
	return values
}

// parseTOMLString parses a TOML value as a string. The value is the raw text
// with surrounding spaces removed. It reports false when the value is not a
// single-line string.
func parseTOMLString(value string) (string, bool) {
	if strings.HasPrefix(value, "'") {
		// Literal string: no escaping inside.
		end := strings.Index(value[1:], "'")
		if end < 0 {
			return "", false
		}
		return value[1 : 1+end], true
	}
	if !strings.HasPrefix(value, `"`) {
		return "", false
	}
	// Basic string: honor the escape sequences which can appear in a URL.
	var out strings.Builder
	for i := 1; i < len(value); i++ {
		ch := value[i]
		if ch == '"' {
			return out.String(), true
		}
		if ch != '\\' {
			out.WriteByte(ch)
			continue
		}
		i++
		if i >= len(value) {
			return "", false
		}
		switch value[i] {
		case 'n':
			out.WriteByte('\n')
		case 't':
			out.WriteByte('\t')
		case 'r':
			out.WriteByte('\r')
		case '"':
			out.WriteByte('"')
		case '\\':
			out.WriteByte('\\')
		default:
			return "", false
		}
	}
	return "", false
}
