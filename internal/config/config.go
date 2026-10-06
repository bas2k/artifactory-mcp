package config

import (
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const DefaultResponseLimit int64 = 5 << 20

type Config struct {
	LogLevel      slog.Level
	Transport     string
	HTTP          HTTPConfig
	URL           string
	Token         string
	Repositories  []string
	DisabledTools []string
	CAFile        string
	Timeout       time.Duration
	ResponseLimit int64
	AllowHTTP     bool
}

func Load(getenv func(string) string, args ...string) (Config, error) {
	flags := flag.NewFlagSet("artifactory-mcp", flag.ContinueOnError)
	disabledTools := flags.String("disable-tools", getenv("MCP_DISABLE_TOOLS"), "Comma-separated MCP tool names to disable (overrides MCP_DISABLE_TOOLS)")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if flags.NArg() != 0 {
		return Config{}, fmt.Errorf("unexpected positional arguments")
	}
	c := Config{URL: getenv("ARTIFACTORY_URL"), Token: getenv("ARTIFACTORY_ACCESS_TOKEN"), CAFile: getenv("ARTIFACTORY_CA_FILE"), Timeout: 30 * time.Second, ResponseLimit: DefaultResponseLimit}
	for _, name := range strings.Split(*disabledTools, ",") {
		if name = strings.TrimSpace(name); name != "" {
			c.DisabledTools = append(c.DisabledTools, name)
		}
	}
	if s := getenv("MCP_LOG_LEVEL"); s != "" {
		if err := c.LogLevel.UnmarshalText([]byte(s)); err != nil {
			return c, fmt.Errorf("MCP_LOG_LEVEL must be debug, info, warn, or error")
		}
	}
	c.Transport = getenv("MCP_TRANSPORT")
	if c.Transport == "" {
		c.Transport = "stdio"
	}
	if c.Transport == "streamable-http" {
		c.HTTP = HTTPConfig{Addr: getenv("MCP_HTTP_ADDR"), AuthToken: getenv("MCP_HTTP_AUTH_TOKEN"), MetricsAuthToken: getenv("MCP_METRICS_AUTH_TOKEN")}
		if c.HTTP.Addr == "" {
			c.HTTP.Addr = "127.0.0.1:8080"
		}
		if s := getenv("MCP_HTTP_ALLOWED_ORIGINS"); s != "" {
			for _, origin := range strings.Split(s, ",") {
				c.HTTP.AllowedOrigins = append(c.HTTP.AllowedOrigins, strings.TrimSpace(origin))
			}
		}
	}
	var err error
	if s := getenv("ARTIFACTORY_ALLOW_HTTP"); s != "" {
		c.AllowHTTP, err = strconv.ParseBool(s)
		if err != nil {
			return c, fmt.Errorf("ARTIFACTORY_ALLOW_HTTP must be a boolean")
		}
	}
	if s := getenv("ARTIFACTORY_REQUEST_TIMEOUT"); s != "" {
		c.Timeout, err = time.ParseDuration(s)
		if err != nil {
			return c, fmt.Errorf("ARTIFACTORY_REQUEST_TIMEOUT must be a duration")
		}
	}
	if s := getenv("ARTIFACTORY_RESPONSE_LIMIT"); s != "" {
		c.ResponseLimit, err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			return c, fmt.Errorf("ARTIFACTORY_RESPONSE_LIMIT must be a byte count")
		}
	}
	if s := getenv("ARTIFACTORY_REPOSITORIES"); s != "" {
		for _, r := range strings.Split(s, ",") {
			c.Repositories = append(c.Repositories, strings.TrimSpace(r))
		}
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	c.URL = strings.TrimRight(c.URL, "/")
	return c, nil
}

func (c Config) Validate() error {
	for _, name := range c.DisabledTools {
		switch name {
		case "get_server_info", "list_repositories", "search_artifacts", "search_artifacts_sorted", "get_artifact_info", "list_folder",
			"get_artifact_properties", "get_artifact_stats", "list_builds", "get_build_info", "list_build_runs", "search_packages", "list_package_versions":
		default:
			return fmt.Errorf("MCP_DISABLE_TOOLS / --disable-tools contains an unknown tool name")
		}
	}
	switch c.LogLevel {
	case slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError:
	default:
		return fmt.Errorf("MCP_LOG_LEVEL must be debug, info, warn, or error")
	}
	switch c.Transport {
	case "", "stdio":
	case "streamable-http":
		if err := c.HTTP.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("MCP_TRANSPORT must be stdio or streamable-http")
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(c.URL, "#") {
		return fmt.Errorf("ARTIFACTORY_URL must be a full service URL without userinfo, query, or fragment")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !c.AllowHTTP) {
		return fmt.Errorf("ARTIFACTORY_URL requires HTTPS (HTTP requires ARTIFACTORY_ALLOW_HTTP=true)")
	}
	if u.Path != "" && u.Path != "/" {
		if err := ValidatePath(strings.Trim(u.Path, "/"), false); err != nil {
			return fmt.Errorf("ARTIFACTORY_URL has an invalid service prefix")
		}
	}
	if c.Token == "" || strings.ContainsAny(c.Token, "\r\n\t ") {
		return fmt.Errorf("ARTIFACTORY_ACCESS_TOKEN must be a nonempty bearer token")
	}
	if c.Timeout <= 0 || c.Timeout > 10*time.Minute {
		return fmt.Errorf("request timeout must be between zero and 10 minutes")
	}
	if c.ResponseLimit <= 0 || c.ResponseLimit > 100<<20 {
		return fmt.Errorf("response limit must be between 1 byte and 100 MiB")
	}
	for _, r := range c.Repositories {
		if err := ValidateSegment(r); err != nil {
			return fmt.Errorf("repository allowlist contains an invalid key")
		}
	}
	return nil
}

func ValidateSegment(s string) error {
	if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\:?#") || strings.Contains(s, "%") || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid path segment")
	}
	return nil
}

func ValidatePath(s string, allowEmpty bool) error {
	if s == "" && allowEmpty {
		return nil
	}
	for _, p := range strings.Split(s, "/") {
		if err := ValidateSegment(p); err != nil {
			return err
		}
	}
	return nil
}

func Allowed(repo string, allowlist []string) bool {
	return len(allowlist) == 0 || slices.Contains(allowlist, repo)
}
