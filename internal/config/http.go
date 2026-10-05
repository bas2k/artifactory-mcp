package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

type HTTPConfig struct {
	Addr             string
	AuthToken        string
	MetricsAuthToken string
	AllowedOrigins   []string
}

func (c HTTPConfig) Validate() error {
	host, port, err := net.SplitHostPort(c.Addr)
	n, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || n < 0 || n > 65535 || strings.IndexFunc(host, unicode.IsSpace) >= 0 {
		return fmt.Errorf("MCP_HTTP_ADDR must be a host:port address with a port between 0 and 65535")
	}
	if strings.IndexFunc(c.AuthToken, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return fmt.Errorf("MCP_HTTP_AUTH_TOKEN must contain only printable ASCII without spaces")
	}
	if strings.IndexFunc(c.MetricsAuthToken, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return fmt.Errorf("MCP_METRICS_AUTH_TOKEN must contain only printable ASCII without spaces")
	}
	for _, origin := range c.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(origin, "#%,*") || strings.IndexFunc(origin, unicode.IsSpace) >= 0 {
			return fmt.Errorf("MCP_HTTP_ALLOWED_ORIGINS must contain exact HTTP(S) origins without paths, credentials, queries, or fragments")
		}
		if strings.HasSuffix(u.Host, ":") {
			return fmt.Errorf("MCP_HTTP_ALLOWED_ORIGINS contains an invalid port")
		}
		if port := u.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("MCP_HTTP_ALLOWED_ORIGINS contains an invalid port")
			}
		}
	}
	return nil
}
