package server

import (
	"net/http/httptest"
	"testing"

	"ghrouter/internal/security"
	"ghrouter/internal/types"
)

func TestACLRejectsMissingToken(t *testing.T) {
	t.Setenv("GHR_ACCESS_TOKEN", "secret")
	srv := New(&types.Config{ACL: types.ACLConfig{Enabled: true}})
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:4321"
	if srv.authorized(req) {
		t.Fatal("expected request without token to be rejected")
	}
}

func TestACLAcceptsBearerTokenAndAllowedIP(t *testing.T) {
	t.Setenv("GHR_ACCESS_TOKEN", "secret")
	srv := New(&types.Config{ACL: types.ACLConfig{Enabled: true, Allow: []string{"127.0.0.1"}}})
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:4321"
	req.Header.Set("Authorization", "Bearer secret")
	if !srv.authorized(req) {
		t.Fatal("expected token and allowlisted IP to be accepted")
	}
}

func TestACLAcceptsAnthropicAPIKeyHeader(t *testing.T) {
	t.Setenv("GHR_ACCESS_TOKEN", "secret")
	srv := New(&types.Config{ACL: types.ACLConfig{Enabled: true}})
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.RemoteAddr = "127.0.0.1:4321"
	req.Header.Set("x-api-key", "secret")
	if !srv.authorized(req) {
		t.Fatal("expected x-api-key to authenticate Anthropic-compatible requests")
	}
}

func TestACLAcceptsGeneratedClientKeyWithoutEnvironmentToken(t *testing.T) {
	t.Setenv("GHR_ACCESS_TOKEN", "")
	srv := New(&types.Config{ACL: types.ACLConfig{Enabled: true}})
	srv.clientKeys = security.ClientKeys{OpenAI: "sk-ghrouter-test"}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:4321"
	req.Header.Set("Authorization", "Bearer sk-ghrouter-test")
	if !srv.authorized(req) {
		t.Fatal("expected generated client key to authenticate without environment token")
	}
}

func TestACLRejectsNonLoopbackWhenAllowlistIsEmpty(t *testing.T) {
	t.Setenv("GHR_ACCESS_TOKEN", "secret")
	srv := New(&types.Config{ACL: types.ACLConfig{Enabled: true}})
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.RemoteAddr = "198.51.100.10:4321"
	req.Header.Set("Authorization", "Bearer secret")
	if srv.authorized(req) {
		t.Fatal("expected empty allowlist to reject non-loopback client")
	}
}

func TestACLScopesGeneratedKeysByClientProtocol(t *testing.T) {
	srv := New(&types.Config{ACL: types.ACLConfig{Enabled: true}})
	srv.clientKeys = security.ClientKeys{GitHub: "gh-key", OpenAI: "oa-key", Anthropic: "an-key"}
	githubMessages := httptest.NewRequest("POST", "/v1/messages", nil)
	githubMessages.RemoteAddr = "127.0.0.1:4321"
	githubMessages.Header.Set("Authorization", "Bearer gh-key")
	if srv.authorized(githubMessages) {
		t.Fatal("expected GitHub key to be rejected on Anthropic endpoint")
	}
	openAIResponses := httptest.NewRequest("POST", "/v1/responses", nil)
	openAIResponses.RemoteAddr = "127.0.0.1:4321"
	openAIResponses.Header.Set("Authorization", "Bearer oa-key")
	if !srv.authorized(openAIResponses) {
		t.Fatal("expected OpenAI key to access Responses endpoint")
	}
	anthropicChat := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	anthropicChat.RemoteAddr = "127.0.0.1:4321"
	anthropicChat.Header.Set("Authorization", "Bearer an-key")
	if srv.authorized(anthropicChat) {
		t.Fatal("expected Anthropic key to be rejected on Chat Completions endpoint")
	}
}
