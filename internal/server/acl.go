package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"os"
	"strings"
)

func (s *Server) authorized(r *http.Request) bool {
	if s == nil || s.cfg == nil || !s.cfg.ACL.Enabled {
		return true
	}
	if !allowedRemote(r.RemoteAddr, s.cfg.ACL.Allow) {
		return false
	}
	envName := s.cfg.ACL.TokenEnv
	if envName == "" {
		envName = "GHR_ACCESS_TOKEN"
	}
	expected := os.Getenv(envName)
	provided := requestToken(r)
	if provided == "" {
		return false
	}
	valid := map[string]string{"github": s.clientKeys.GitHub, "openai": s.clientKeys.OpenAI, "anthropic": s.clientKeys.Anthropic}
	if expected != "" {
		valid["admin"] = expected
	}
	for kind, candidate := range valid {
		if candidate != "" && len(provided) == len(candidate) && subtle.ConstantTimeCompare([]byte(provided), []byte(candidate)) == 1 {
			if kind != "admin" && !s.scopeAllows(kind, r.URL.Path) {
				return false
			}
			return true
		}
	}
	return false
}

func (s *Server) adminAuthorized(r *http.Request) bool {
	if s == nil || s.cfg == nil || !allowedRemote(r.RemoteAddr, s.cfg.ACL.Allow) {
		return false
	}
	envName := s.cfg.ACL.TokenEnv
	if envName == "" {
		envName = "GHR_ACCESS_TOKEN"
	}
	expected := strings.TrimSpace(os.Getenv(envName))
	provided := requestToken(r)
	return expected != "" && provided != "" && len(provided) == len(expected) && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func requestToken(r *http.Request) string {
	provided := strings.TrimSpace(r.Header.Get("X-Ghrouter-Token"))
	if provided == "" {
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, prefix) {
			provided = strings.TrimSpace(strings.TrimPrefix(auth, prefix))
		}
	}
	if provided == "" {
		provided = strings.TrimSpace(r.Header.Get("x-api-key"))
	}
	return provided
}

func (s *Server) scopeAllows(kind, path string) bool {
	if scopes := s.cfg.ACL.Scopes[kind]; len(scopes) > 0 {
		for _, allowed := range scopes {
			if allowed == path {
				return true
			}
		}
		return false
	}
	defaults := map[string][]string{
		"github":    {"/v1/chat/completions", "/v1/models"},
		"openai":    {"/v1/chat/completions", "/v1/responses", "/v1/models"},
		"anthropic": {"/v1/messages", "/v1/models"},
	}
	for _, allowed := range defaults[kind] {
		if allowed == path {
			return true
		}
	}
	return false
}

func allowedRemote(remote string, allow []string) bool {
	if len(allow) == 0 {
		host, _, err := net.SplitHostPort(remote)
		if err != nil {
			host = remote
		}
		return net.ParseIP(strings.Trim(host, "[]")).IsLoopback()
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	for _, entry := range allow {
		entry = strings.TrimSpace(entry)
		if entry == host {
			return true
		}
		if _, network, err := net.ParseCIDR(entry); err == nil && ip != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}
