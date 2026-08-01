package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ghrouter/internal/providers"
	"ghrouter/internal/types"
)

type Server struct {
	cfg       *types.Config
	providers map[string]*providers.ProviderRunner
	mu        sync.RWMutex
	httpSrv   *http.Server
	started   time.Time
	requests  int64
}

func New(cfg *types.Config) *Server {
	s := &Server{cfg: cfg, providers: make(map[string]*providers.ProviderRunner), started: time.Now()}
	for _, p := range cfg.Providers {
		if p.Enabled {
			s.providers[p.Name] = providers.NewProviderRunner(p)
		}
	}
	return s
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	mux.HandleFunc("/v1/messages", s.handleAnthropicMessages)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/", s.handleRoot)

	port := s.cfg.ListenPort
	if port == 0 {
		port = 9090
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	s.httpSrv = &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.httpSrv.Shutdown(shutdownCtx)
	}()

	return s.httpSrv.ListenAndServe()
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req types.OpenAIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid_request", err.Error())
		return
	}

	provider, model := s.route(req.Model)
	if provider == "" {
		writeError(w, 404, "model_not_found", fmt.Sprintf("no provider for model %q", req.Model))
		return
	}

	runner := s.getProvider(provider)
	if runner == nil {
		writeError(w, 500, "provider_unavailable", fmt.Sprintf("provider %s not started", provider))
		return
	}

	stream := req.Stream != nil && *req.Stream
	if stream {
		s.streamChat(r.Context(), w, runner, &req, model)
	} else {
		s.nonStreamChat(r.Context(), w, runner, &req, model)
	}
}

// route maps a requested model (or empty) to provider + concrete model
func (s *Server) route(requested string) (provider string, model string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// explicit provider prefix wins
	for _, p := range s.cfg.Providers {
		if !p.Enabled {
			continue
		}
		pref := prefixFor(p.Type)
		if pref != "" && strings.HasPrefix(requested, pref) {
			rest := strings.TrimPrefix(requested, pref)
			model = s.resolveModel(p, rest)
			return p.Name, model
		}
	}

	// empty model -> first healthy provider
	if requested == "" {
		for _, p := range s.cfg.Providers {
			if !p.Enabled {
				continue
			}
			if len(p.Models) > 0 {
				return p.Name, p.Models[0]
			}
		}
		return "", ""
	}

	// exact model match across providers
	for _, p := range s.cfg.Providers {
		if !p.Enabled {
			continue
		}
		if model = s.resolveModel(p, requested); model != "" {
			return p.Name, model
		}
	}

	// route table fallback
	for _, route := range s.cfg.Routes {
		if matchPattern(requested, route.Pattern) {
			return route.Provider, requested
		}
	}

	return "", ""
}

func (s *Server) resolveModel(p *types.Provider, name string) string {
	if name == "" {
		return ""
	}
	for _, m := range p.Models {
		if strings.HasSuffix(m, name) || strings.EqualFold(m, name) {
			return m
		}
	}
	return ""
}

func matchPattern(name, pattern string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, strings.TrimSuffix(pattern, "*"))
	}
	return strings.EqualFold(name, pattern)
}

func (s *Server) getProvider(name string) *providers.ProviderRunner {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.providers[name]
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	s.mu.RLock()
	defer s.mu.RUnlock()

	type modelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	seen := map[string]bool{}
	var data []modelEntry
	for _, p := range s.cfg.Providers {
		if !p.Enabled {
			continue
		}
		for _, m := range p.Models {
			if seen[m] {
				continue
			}
			seen[m] = true
			data = append(data, modelEntry{ID: m, Object: "model", Created: s.started.Unix(), OwnedBy: p.Name})
		}
	}
	sort.Slice(data, func(i, j int) bool { return data[i].ID < data[j].ID })
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "uptime": time.Since(s.started).String()})
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "ghrouter: OpenAI-compatible router for gh copilot.\nGET /v1/models, POST /v1/chat/completions, GET /health\n")
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": code, "message": msg}})
}

func prefixFor(provider types.ProviderType) string {
	m := map[types.ProviderType]string{
		types.ProviderClaudeCode: "cc/", types.ProviderCodex: "cx/",
		types.ProviderOpenCode: "oc/", types.ProviderMimo: "mi/", types.ProviderPi: "pi/",
	}
	return m[provider]
}
