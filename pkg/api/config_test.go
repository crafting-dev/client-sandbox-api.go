package api

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// configDir creates a configuration folder for the test and points
// EnvConfigDir at it.
func configDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	// Leave the rest of the environment out of the resolution.
	t.Setenv(EnvServerURL, "")
	t.Setenv(EnvAuthToken, "")
	t.Setenv(EnvAuthTokenFile, "")
	return dir
}

// writeConfig writes the config file in the test's configuration folder.
func writeConfig(t *testing.T, dir string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", ConfigFile, err)
	}
}

func TestDefaultConfigFolder(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home folder: %v", err)
	}
	want := filepath.Join(home, ".config", "crafting", "sandbox")
	if runtime.GOOS == "darwin" {
		want = filepath.Join(home, ".crafting", "sandbox")
	}
	if got := defaultConfigFolder(); got != want {
		t.Errorf("defaultConfigFolder() = %q, want %q", got, want)
	}
}

func TestResolveConfigFolderFromEnv(t *testing.T) {
	dir := configDir(t)
	if got := resolveConfigFolder(); got != dir {
		t.Errorf("resolveConfigFolder() = %q, want %q", got, dir)
	}
}

func TestResolveConfigFolderDefaultsWhenEnvEmpty(t *testing.T) {
	t.Setenv(EnvConfigDir, "")
	if got := resolveConfigFolder(); got != defaultConfigFolder() {
		t.Errorf("resolveConfigFolder() = %q, want the default %q", got, defaultConfigFolder())
	}
}

func TestServerURLDefault(t *testing.T) {
	configDir(t)
	config, err := resolveConfiguration("")
	if err != nil {
		t.Fatalf("resolveConfiguration: %v", err)
	}
	if config.serverURL != DefaultServerURL {
		t.Errorf("serverURL = %q, want %q", config.serverURL, DefaultServerURL)
	}
	if config.grpcTarget != "sandboxes.cloud:443" {
		t.Errorf("grpcTarget = %q, want sandboxes.cloud:443", config.grpcTarget)
	}
	if !config.secure {
		t.Error("secure = false, want true for an https server URL")
	}
}

func TestServerURLFromOption(t *testing.T) {
	configDir(t)
	config, err := resolveConfiguration("https://myorg.sandboxes.site")
	if err != nil {
		t.Fatalf("resolveConfiguration: %v", err)
	}
	if config.serverURL != "https://myorg.sandboxes.site" {
		t.Errorf("serverURL = %q, want the option", config.serverURL)
	}
}

func TestServerURLFromConfigFile(t *testing.T) {
	dir := configDir(t)
	writeConfig(t, dir, "# a comment\nserver_url = \"https://from-file.example.com\"\n")

	config, err := resolveConfiguration("")
	if err != nil {
		t.Fatalf("resolveConfiguration: %v", err)
	}
	if config.serverURL != "https://from-file.example.com" {
		t.Errorf("serverURL = %q, want the config file value", config.serverURL)
	}
}

func TestServerURLOptionOverridesConfigFile(t *testing.T) {
	dir := configDir(t)
	writeConfig(t, dir, "server_url = \"https://from-file.example.com\"\n")

	config, err := resolveConfiguration("https://from-option.example.com")
	if err != nil {
		t.Fatalf("resolveConfiguration: %v", err)
	}
	if config.serverURL != "https://from-option.example.com" {
		t.Errorf("serverURL = %q, want the option to win over the config file", config.serverURL)
	}
}

func TestServerURLEnvOverridesEverything(t *testing.T) {
	dir := configDir(t)
	writeConfig(t, dir, "server_url = \"https://from-file.example.com\"\n")
	t.Setenv(EnvServerURL, "https://from-env.example.com")

	config, err := resolveConfiguration("https://from-option.example.com")
	if err != nil {
		t.Fatalf("resolveConfiguration: %v", err)
	}
	if config.serverURL != "https://from-env.example.com" {
		t.Errorf("serverURL = %q, want the environment to win", config.serverURL)
	}
}

func TestServerURLNormalization(t *testing.T) {
	configDir(t)
	for _, testCase := range []struct {
		name       string
		serverURL  string
		wantURL    string
		wantTarget string
		wantSecure bool
	}{
		{"trailing slash", "https://example.com/", "https://example.com", "example.com:443", true},
		{"trailing slashes", "https://example.com///", "https://example.com", "example.com:443", true},
		{"surrounding spaces", "  https://example.com  ", "https://example.com", "example.com:443", true},
		{"explicit port", "https://example.com:8443", "https://example.com:8443", "example.com:8443", true},
		{"cleartext", "http://example.com", "http://example.com", "example.com:80", false},
		{"cleartext with port", "http://127.0.0.1:8080", "http://127.0.0.1:8080", "127.0.0.1:8080", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			config, err := resolveConfiguration(testCase.serverURL)
			if err != nil {
				t.Fatalf("resolveConfiguration(%q): %v", testCase.serverURL, err)
			}
			if config.serverURL != testCase.wantURL {
				t.Errorf("serverURL = %q, want %q", config.serverURL, testCase.wantURL)
			}
			if config.grpcTarget != testCase.wantTarget {
				t.Errorf("grpcTarget = %q, want %q", config.grpcTarget, testCase.wantTarget)
			}
			if config.secure != testCase.wantSecure {
				t.Errorf("secure = %v, want %v", config.secure, testCase.wantSecure)
			}
		})
	}
}

func TestServerURLRejected(t *testing.T) {
	configDir(t)
	for _, testCase := range []struct{ name, serverURL string }{
		{"unsupported scheme", "ftp://example.com"},
		{"no scheme", "example.com"},
		{"no host", "https://"},
		{"not a URL", "://"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := resolveConfiguration(testCase.serverURL); err == nil {
				t.Errorf("resolveConfiguration(%q) succeeded, want an error", testCase.serverURL)
			}
		})
	}
}

func TestUnreadableConfigFileFallsThrough(t *testing.T) {
	dir := configDir(t)
	// A folder where the config file is expected is not readable as a file.
	if err := os.Mkdir(filepath.Join(dir, ConfigFile), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	config, err := resolveConfiguration("")
	if err != nil {
		t.Fatalf("resolveConfiguration: %v", err)
	}
	if config.serverURL != DefaultServerURL {
		t.Errorf("serverURL = %q, want the default", config.serverURL)
	}
}

func TestParseTOMLStrings(t *testing.T) {
	content := strings.Join([]string{
		`# a comment`,
		``,
		`server_url = "https://example.com"`,
		`literal = 'https://literal.example.com'`,
		`quoted_key = "value"`,
		`escaped = "a\tb"`,
		`number = 42`,
		`[table]`,
		`server_url = "https://ignored.example.com"`,
	}, "\n")

	values := parseTOMLStrings(content)
	for key, want := range map[string]string{
		"server_url": "https://example.com",
		"literal":    "https://literal.example.com",
		"quoted_key": "value",
		"escaped":    "a\tb",
	} {
		if got := values[key]; got != want {
			t.Errorf("values[%q] = %q, want %q", key, got, want)
		}
	}
	if _, ok := values["number"]; ok {
		t.Error("a non-string value was parsed, want it ignored")
	}
}

func TestParseTOMLStringRejectsUnterminated(t *testing.T) {
	for _, value := range []string{`"unterminated`, `'unterminated`, `"bad \escape"`, `unquoted`} {
		if got, ok := parseTOMLString(value); ok {
			t.Errorf("parseTOMLString(%q) = %q, true; want false", value, got)
		}
	}
}
