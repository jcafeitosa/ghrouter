package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ghrouter/internal/catalog"
	"ghrouter/internal/health"
	"ghrouter/internal/providers"
	"ghrouter/internal/storage"
	"ghrouter/internal/types"
)

func TestCatalogVerificationProvenanceSurvivesRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "ghrouter.db")
	discoveredAt := time.Now().UTC().Add(-2 * time.Hour).Round(0)
	verifiedAt := time.Now().UTC().Add(-time.Hour).Round(0)
	verificationError := "provider verification timed out"

	provider := func(withMetadata bool) *types.Provider {
		p := &types.Provider{
			Name:    "fixture",
			Type:    types.ProviderCustom,
			Models:  []string{"verified-model", "failed-model"},
			Enabled: true,
		}
		if withMetadata {
			p.ModelInfo = map[string]types.ModelInfo{
				"verified-model": {
					Source:       "native",
					DiscoveredAt: discoveredAt,
					VerifiedAt:   verifiedAt,
				},
				"failed-model": {
					Source:            "native",
					DiscoveredAt:      discoveredAt,
					HealthStatus:      "failed",
					VerificationError: verificationError,
				},
			}
		}
		return p
	}

	first := NewWithConfigPath(&types.Config{
		Storage:   types.StorageConfig{Enabled: true, Path: databasePath},
		Providers: []*types.Provider{provider(true)},
	}, filepath.Join(t.TempDir(), "first-config.yaml"))
	first.catalog.GetModel("fixture/verified-model").ToolUse = true
	for _, sample := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond} {
		first.catalog.RecordLatency("fixture/verified-model", sample)
	}
	if err := first.PersistCurrentState(); err != nil {
		t.Fatalf("persist initial catalog: %v", err)
	}

	second := NewWithConfigPath(&types.Config{
		Storage:   types.StorageConfig{Enabled: true, Path: databasePath},
		Providers: []*types.Provider{provider(false)},
	}, filepath.Join(t.TempDir(), "second-config.yaml"))
	store, err := storage.Open(storage.Config{Enabled: true, Path: databasePath})
	if err != nil {
		t.Fatalf("open persisted catalog: %v", err)
	}
	defer store.Close()
	if err := second.restoreCatalogState(store); err != nil {
		t.Fatalf("restore catalog: %v", err)
	}

	verified := second.catalog.GetModel("fixture/verified-model")
	if verified == nil {
		t.Fatal("verified model missing after restart")
	}
	if !verified.Info.DiscoveredAt.Equal(discoveredAt) || !verified.Info.VerifiedAt.Equal(verifiedAt) {
		t.Fatalf("verified provenance not restored: discovered=%v verified=%v", verified.Info.DiscoveredAt, verified.Info.VerifiedAt)
	}
	if verified.Info.Provenance() != types.ModelProvenanceVerified {
		t.Fatalf("expected verified provenance, got %q", verified.Info.Provenance())
	}
	if !second.modelVerified("fixture", "verified-model") || !second.modelRoutable("fixture", "verified-model") {
		t.Fatal("restored verified model is not routable after restart")
	}
	if restored := second.catalog.GetModel("fixture/verified-model"); restored == nil || restored.LatencyP50 != 200*time.Millisecond || restored.LatencyP95 != 500*time.Millisecond {
		t.Fatalf("observed latency was not restored after restart: %+v", restored)
	}
	if restored := second.catalog.GetModel("fixture/verified-model"); restored == nil || restored.ToolUse {
		t.Fatalf("live provider capability must override stale persisted tool-use metadata: %+v", restored)
	}
	routeProvider, routeModel := second.RouteOpenAIRequest(&types.OpenAIRequest{Model: "fixture/verified-model"})
	if routeProvider != "fixture" || routeModel != "verified-model" {
		t.Fatalf("restored verified route mismatch: provider=%q model=%q", routeProvider, routeModel)
	}
	modelsResponse := httptest.NewRecorder()
	second.handleModels(modelsResponse, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if modelsResponse.Code != http.StatusOK {
		t.Fatalf("expected restored model list status 200, got %d", modelsResponse.Code)
	}
	if !strings.Contains(modelsResponse.Body.String(), "fixture/verified-model") {
		t.Fatalf("verified model missing from restored /v1/models response: %s", modelsResponse.Body.String())
	}

	failed := second.catalog.GetModel("fixture/failed-model")
	if failed == nil {
		t.Fatal("failed model missing after restart")
	}
	if !failed.Info.DiscoveredAt.Equal(discoveredAt) || !failed.Info.VerifiedAt.IsZero() {
		t.Fatalf("failed verification evidence was not restored: %+v", failed.Info)
	}
	if failed.Info.VerificationError != verificationError {
		t.Fatalf("expected verification error %q, got %q", verificationError, failed.Info.VerificationError)
	}
	if second.modelVerified("fixture", "failed-model") {
		t.Fatal("failed verification was incorrectly promoted to verified after restart")
	}
	if strings.Contains(modelsResponse.Body.String(), "fixture/failed-model") {
		t.Fatal("failed model was incorrectly advertised after restart")
	}
}

func TestListenAndServeCancelsInflightProviderBeforeReturning(t *testing.T) {
	tmpDir := t.TempDir()
	startedPath := filepath.Join(tmpDir, "started")
	cliPath := filepath.Join(tmpDir, "blocking-cli")
	script := "#!/bin/sh\nprintf started > " + startedPath + "\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()

	srv := New(&types.Config{
		ListenPort: port,
		Server:     types.ServerConfig{Host: "127.0.0.1"},
		Health:     types.HealthConfig{Enabled: func() *bool { value := false; return &value }()},
		Providers: []*types.Provider{{
			Name:    "blocking",
			Type:    types.ProviderCustom,
			CLIPath: cliPath,
			Models:  []string{"model"},
			Timeout: 30 * time.Second,
			Enabled: true,
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.ListenAndServe(ctx) }()

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)
	probeClient := &http.Client{Timeout: 100 * time.Millisecond}
	for {
		response, probeErr := probeClient.Get(baseURL + "/livez")
		if response != nil {
			_ = response.Body.Close()
		}
		if probeErr == nil {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("server did not initialize")
		case <-time.After(5 * time.Millisecond):
		}
	}

	clientDone := make(chan error, 1)
	go func() {
		request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", strings.NewReader(`{"model":"model","messages":[{"role":"user","content":"wait"}]}`))
		if err != nil {
			clientDone <- err
			return
		}
		response, err := (&http.Client{}).Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		clientDone <- err
	}()

	started := time.NewTimer(2 * time.Second)
	defer started.Stop()
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		select {
		case <-started.C:
			t.Fatal("provider request did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-clientDone:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request survived service cancellation")
	}
	select {
	case err := <-serveDone:
		if err != nil && !strings.Contains(err.Error(), "Server closed") {
			t.Fatalf("server returned unexpected error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("server returned too late after canceling in-flight request")
	}
}

func TestHandleModelsUsesCanonicalIDsAndSkipsCooldown(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{
				Name:    "claude-code",
				Type:    types.ProviderClaudeCode,
				Enabled: true,
				Models:  []string{"claude-sonnet-5"},
			},
			{
				Name:    "codex",
				Type:    types.ProviderCodex,
				Enabled: true,
				Models:  []string{"gpt-5"},
				ModelInfo: map[string]types.ModelInfo{
					"cx/gpt-5": {Source: "configured", HealthStatus: "healthy", VerifiedAt: time.Now().UTC()},
				},
			},
		},
	})

	srv.catalog.SetCooldown("cc/claude-sonnet-5", time.Now().Add(time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	srv.handleModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `"cooldown_until":"0001-01-01T00:00:00Z"`) {
		t.Fatalf("zero cooldown must be omitted from /v1/models: %s", rec.Body.String())
	}
	var payload struct {
		Object string `json:"object"`
		Data   []struct {
			ID           string                     `json:"id"`
			Object       string                     `json:"object"`
			Created      int64                      `json:"created"`
			OwnedBy      string                     `json:"owned_by"`
			Provenance   string                     `json:"provenance"`
			ToolUse      bool                       `json:"tool_use"`
			Capabilities map[string]map[string]bool `json:"capabilities"`
			List         bool                       `json:"list"`
			Members      []string                   `json:"members"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal model list: %v", err)
	}
	if payload.Object != "list" {
		t.Fatalf("expected list object, got %+v", payload)
	}
	var directModels []string
	lists := map[string][]string{}
	for _, entry := range payload.Data {
		if entry.Object != "model" {
			t.Fatalf("expected model object, got %+v", entry)
		}
		if entry.ID == "cx/gpt-5" && entry.Provenance != "verified" {
			t.Fatalf("expected verified provenance for catalog model, got %+v", entry)
		}
		if entry.List {
			lists[entry.ID] = append([]string(nil), entry.Members...)
			if entry.ID == "ghrouter/tool-use" && !entry.ToolUse {
				t.Fatalf("expected tool-use virtual model to advertise tool capability, got %+v", entry)
			}
			if entry.ID == "ghrouter/tool-use" && !entry.Capabilities["supports"]["tools"] {
				t.Fatalf("expected tool-use virtual model wire capability, got %+v", entry.Capabilities)
			}
			continue
		}
		directModels = append(directModels, entry.ID)
		if entry.ID == "cc/claude-sonnet-5" {
			t.Fatalf("expected cooldowned model to be omitted, got %+v", entry)
		}
	}
	if len(directModels) != 1 || directModels[0] != "cx/gpt-5" {
		t.Fatalf("expected only eligible canonical direct model, got %+v", directModels)
	}
	if members := lists["ghrouter/auto"]; len(members) != 1 || members[0] != "cx/gpt-5" {
		t.Fatalf("expected automatic list to keep only eligible canonical model, got %+v", members)
	}
	if members := lists["ghrouter/codex"]; len(members) != 1 || members[0] != "cx/gpt-5" {
		t.Fatalf("expected provider list to keep only eligible canonical model, got %+v", members)
	}
}

func TestHandleModelsSkipsUnhealthyVirtualListMembers(t *testing.T) {
	verifiedAt := time.Now().UTC()
	srv := NewWithConfigPath(&types.Config{
		Providers: []*types.Provider{{
			Name:    "opencode",
			Type:    types.ProviderOpenCode,
			CLIPath: "/bin/true",
			Enabled: true,
			Models:  []string{"oc/healthy", "oc/failed"},
			ModelInfo: map[string]types.ModelInfo{
				"oc/healthy": {Source: "native", VerifiedAt: verifiedAt, HealthStatus: "healthy"},
				"oc/failed":  {Source: "native", VerifiedAt: verifiedAt, HealthStatus: "failed"},
			},
		}},
		ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"oc/healthy", "oc/failed"}}},
	}, filepath.Join(t.TempDir(), "config.yaml"))

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	srv.handleModels(rec, req)

	var payload struct {
		Data []struct {
			ID      string   `json:"id"`
			List    bool     `json:"list"`
			Members []string `json:"members"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal model list: %v", err)
	}
	for _, entry := range payload.Data {
		if entry.ID != "ghrouter/auto" {
			continue
		}
		if len(entry.Members) != 1 || entry.Members[0] != "oc/healthy" {
			t.Fatalf("expected only healthy member in automatic list, got %+v", entry.Members)
		}
		return
	}
	t.Fatal("expected ghrouter/auto in model list")
}

func TestExplainExplicitUnverifiedModelMatchesExecution(t *testing.T) {
	srv := NewWithConfigPath(&types.Config{Providers: []*types.Provider{{
		Name: "opencode", Type: types.ProviderOpenCode, CLIPath: "/bin/true", Models: []string{"oc/discovered"},
		ModelInfo: map[string]types.ModelInfo{"oc/discovered": {Source: "native"}}, Enabled: true,
	}}}, filepath.Join(t.TempDir(), "config.yaml"))
	req := &types.OpenAIRequest{Model: "oc/discovered"}
	provider, model := srv.RouteOpenAIRequest(req)
	if provider != "opencode" || model != "oc/discovered" {
		t.Fatalf("expected explicit route, got %q/%q", provider, model)
	}
	explanation := srv.ExplainRequest(req)
	if explanation.Selected == nil || !explanation.Selected.Eligible || explanation.Selected.ID != "oc/discovered" {
		t.Fatalf("explain did not reflect executable explicit route: %+v", explanation)
	}
}

func TestHandleModelsReturnsEmptyArrayWhenNoModelIsRoutable(t *testing.T) {
	srv := NewWithConfigPath(&types.Config{
		Providers: []*types.Provider{{
			Name:    "opencode",
			Type:    types.ProviderOpenCode,
			CLIPath: "/bin/true",
			Models:  []string{"oc/discovered"},
			ModelInfo: map[string]types.ModelInfo{
				"oc/discovered": {Source: "native"},
			},
			Enabled: false,
		}},
	}, filepath.Join(t.TempDir(), "config.yaml"))

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	srv.handleModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal model list: %v", err)
	}
	if string(payload.Data) != "[]" {
		t.Fatalf("expected empty JSON array, got %s", payload.Data)
	}
}

func TestExplicitUnverifiedNativeModelIsAdvertisedAndRoutable(t *testing.T) {
	srv := NewWithConfigPath(&types.Config{Providers: []*types.Provider{{
		Name:    "opencode",
		Type:    types.ProviderOpenCode,
		CLIPath: "/bin/true",
		Models:  []string{"oc/discovered"},
		ModelInfo: map[string]types.ModelInfo{
			"oc/discovered": {Source: "native"},
		},
		Enabled: true,
	}}}, filepath.Join(t.TempDir(), "config.yaml"))
	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "oc/discovered"})
	if provider != "opencode" || model != "oc/discovered" {
		t.Fatalf("expected explicit native model route, got %q/%q", provider, model)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	srv.handleModels(rec, req)
	if !strings.Contains(rec.Body.String(), `"id":"oc/discovered"`) || !strings.Contains(rec.Body.String(), `"health":"unknown"`) {
		t.Fatalf("expected unverified native model in unknown inventory, got %s", rec.Body.String())
	}
}

func TestModelsFunctionalOnlyFiltersUnverifiedInventory(t *testing.T) {
	verifiedAt := time.Now().UTC()
	srv := NewWithConfigPath(&types.Config{Providers: []*types.Provider{{
		Name:    "opencode",
		Type:    types.ProviderOpenCode,
		CLIPath: "/bin/true",
		Models:  []string{"oc/verified", "oc/unverified"},
		ModelInfo: map[string]types.ModelInfo{
			"oc/verified":   {Source: "native", VerifiedAt: verifiedAt, HealthStatus: "healthy"},
			"oc/unverified": {Source: "native", HealthStatus: "healthy"},
		},
		Enabled: true,
	}}}, filepath.Join(t.TempDir(), "config.yaml"))

	req := httptest.NewRequest(http.MethodGet, "/v1/models?functional_only=true", nil)
	rec := httptest.NewRecorder()
	srv.handleModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0].ID != "oc/verified" {
		t.Fatalf("expected only verified functional model, got %+v", payload.Data)
	}
}

func TestFunctionalModelListExcludesDegradedCatalogModel(t *testing.T) {
	verifiedAt := time.Now().UTC()
	cfg := &types.Config{Providers: []*types.Provider{{
		Name: "opencode", Type: types.ProviderOpenCode, CLIPath: "/bin/true", Models: []string{"oc/healthy", "oc/degraded"}, Enabled: true,
		ModelInfo: map[string]types.ModelInfo{
			"oc/healthy":  {Source: "native", HealthStatus: "healthy", VerifiedAt: verifiedAt},
			"oc/degraded": {Source: "native", HealthStatus: "degraded", VerifiedAt: verifiedAt},
		},
	}}, ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"oc/healthy", "oc/degraded"}}}}
	srv := NewWithConfigPath(cfg, filepath.Join(t.TempDir(), "config.yaml"))

	members := srv.functionalModelListMembers(cfg.ModelLists[0])
	if len(members) != 1 || members[0] != "oc/healthy" {
		t.Fatalf("expected only healthy member in functional list, got %+v", members)
	}
}

func TestFunctionalModelListCanonicalizesProviderMembers(t *testing.T) {
	verifiedAt := time.Now().UTC()
	cfg := &types.Config{
		Providers: []*types.Provider{{
			Name: "fixture", Type: types.ProviderCustom, CLIPath: "/bin/true", Models: []string{"fixture-model"}, Enabled: true,
			ModelInfo: map[string]types.ModelInfo{
				"fixture-model": {Source: "configured", HealthStatus: "healthy", VerifiedAt: verifiedAt},
			},
		}},
		ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"fixture-model"}}},
	}
	srv := NewWithConfigPath(cfg, filepath.Join(t.TempDir(), "config.yaml"))

	members := srv.functionalModelListMembers(cfg.ModelLists[0])
	if len(members) != 1 || members[0] != "fixture/fixture-model" {
		t.Fatalf("expected canonical provider member, got %+v", members)
	}
	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "ghrouter/auto"})
	if provider != "fixture" || model != "fixture-model" {
		t.Fatalf("expected canonical list to remain routable, got %q/%q", provider, model)
	}
}

func TestModelsRejectsInvalidFunctionalOnlyFilter(t *testing.T) {
	srv := New(&types.Config{})
	req := httptest.NewRequest(http.MethodGet, "/v1/models?functional_only=maybe", nil)
	rec := httptest.NewRecorder()
	srv.handleModels(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid functional_only, got %d", rec.Code)
	}
}

func TestChatRejectsEmptyMessages(t *testing.T) {
	srv := New(&types.Config{})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"mlx-community/gemma-4-e2b-it-4bit","messages":[]}`))
	rec := httptest.NewRecorder()
	srv.handleChat(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty messages, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAutomaticRoutingSkipsUnverifiedConfiguredModel(t *testing.T) {
	srv := NewWithConfigPath(&types.Config{Providers: []*types.Provider{{
		Name:    "opencode",
		Type:    types.ProviderOpenCode,
		CLIPath: "/bin/true",
		Models:  []string{"oc/unverified"},
		ModelInfo: map[string]types.ModelInfo{
			"oc/unverified": {Source: "native"},
		},
		Enabled: true,
	}}}, filepath.Join(t.TempDir(), "config.yaml"))

	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{})
	if provider != "" || model != "" {
		t.Fatalf("expected automatic routing to skip unverified model, got %q/%q", provider, model)
	}

	provider, model = srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "oc/unverified"})
	if provider != "opencode" || model != "oc/unverified" {
		t.Fatalf("expected explicit request to remain addressable, got %q/%q", provider, model)
	}
}

func TestCanonicalProviderNameRoutesExplicitLocalModel(t *testing.T) {
	now := time.Now().UTC()
	srv := New(&types.Config{Providers: []*types.Provider{{
		Name: "local-brain", Type: types.ProviderLocal, BaseURL: "http://127.0.0.1:19122",
		Models: []string{"mlx-community/Qwen3.5-0.8B-OptiQ-4bit"}, Enabled: true,
		ModelInfo: map[string]types.ModelInfo{
			"mlx-community/Qwen3.5-0.8B-OptiQ-4bit": {
				Provider: "local-brain", Model: "mlx-community/Qwen3.5-0.8B-OptiQ-4bit",
				Source: "native", VerifiedAt: now, HealthStatus: "healthy",
			},
		},
	}}})
	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "local-brain/mlx-community/Qwen3.5-0.8B-OptiQ-4bit"})
	if provider != "local-brain" || model != "mlx-community/Qwen3.5-0.8B-OptiQ-4bit" {
		t.Fatalf("expected canonical local model route, got %q/%q", provider, model)
	}
}

func TestConfiguredLocalBrainIsNotRoutableBeforeVerifiedAttach(t *testing.T) {
	srv := NewWithConfigPath(&types.Config{Providers: []*types.Provider{{
		Name: "local-brain", Type: types.ProviderLocal, BaseURL: "http://127.0.0.1:19122",
		Models: []string{"mlx-community/Qwen3.5-0.8B-OptiQ-4bit"}, Enabled: true,
		ModelInfo: map[string]types.ModelInfo{
			"mlx-community/Qwen3.5-0.8B-OptiQ-4bit": {
				Source: "native", VerifiedAt: time.Now().UTC(), HealthStatus: "healthy",
			},
		},
	}}}, filepath.Join(t.TempDir(), "config.yaml"))
	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "local-brain/mlx-community/Qwen3.5-0.8B-OptiQ-4bit"})
	if provider != "" || model != "" {
		t.Fatalf("expected local model to wait for verified attach, got %q/%q", provider, model)
	}
}

func TestAutomaticModelBootstrapRequiresExplicitVerificationStartup(t *testing.T) {
	cfg := &types.Config{Providers: []*types.Provider{{
		Name:    "opencode",
		Type:    types.ProviderOpenCode,
		CLIPath: "/bin/true",
		Models:  []string{"oc/discovered"},
		Enabled: true,
	}}}
	srv := NewWithConfigPath(cfg, filepath.Join(t.TempDir(), "config.yaml"))
	if srv.autoBootstrapEnabled() {
		t.Fatal("expected omitted verification settings to keep startup probes disabled")
	}
	enabled := true
	cfg.Verification.Enabled = &enabled
	if srv.autoBootstrapEnabled() {
		t.Fatal("expected startup probes to remain disabled until startup is explicitly enabled")
	}
	cfg.Verification.Startup = true
	if !srv.autoBootstrapEnabled() {
		t.Fatal("expected explicit verification startup to enable the bootstrap")
	}
}

func TestModelProbeResolvesAbsoluteConfiguredModelID(t *testing.T) {
	model := "/Users/tester/.lmstudio/models/mlx-community/gemma-4-e2b-it-4bit"
	srv := New(&types.Config{Providers: []*types.Provider{{
		Name: "local-brain", Type: types.ProviderLocal, Models: []string{model}, Enabled: true,
	}}})
	provider, resolved := srv.resolveProbeTarget(model)
	if provider != "local-brain" || resolved != model {
		t.Fatalf("expected exact absolute model resolution, got %q/%q", provider, resolved)
	}
}

func TestModelProbeAcceptsAnyShortHealthyResponse(t *testing.T) {
	for _, output := range []string{"OK", "hello", "Yes", "OK, how can I help?", "prefix GHROUTER_MODEL_PROBE_OK suffix"} {
		if !validModelProbeOutput(output) {
			t.Fatalf("expected successful probe response %q to be accepted", output)
		}
	}
	for _, output := range []string{"", "provider error", "request failed", "timed out", strings.Repeat("x", 4097)} {
		if validModelProbeOutput(output) {
			t.Fatalf("expected invalid probe response %q to be rejected", output)
		}
	}
}

func TestLiveSnapshotIncludesConfiguredModelInfoOnlyAndExcludesCooldown(t *testing.T) {
	cfg := &types.Config{
		Providers: []*types.Provider{
			{
				Name:    "opencode",
				Type:    types.ProviderOpenCode,
				CLIPath: "/bin/true",
				Enabled: true,
				ModelInfo: map[string]types.ModelInfo{
					"oc/good": {
						Source:        "configured",
						VerifiedAt:    time.Unix(100, 0).UTC(),
						HealthStatus:  "healthy",
						ContextWindow: 1_000_000,
						Thinking:      true,
					},
					"oc/bad": {
						Source:        "configured",
						HealthStatus:  "failed",
						CooldownUntil: time.Now().Add(time.Hour),
					},
				},
			},
		},
	}
	srv := NewWithConfigPath(cfg, filepath.Join(t.TempDir(), "config.yaml"))

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	srv.handleModels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload struct {
		Object string `json:"object"`
		Data   []struct {
			ID         string   `json:"id"`
			Object     string   `json:"object"`
			Created    int64    `json:"created"`
			OwnedBy    string   `json:"owned_by"`
			Provenance string   `json:"provenance"`
			List       bool     `json:"list"`
			Members    []string `json:"members"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal model list: %v", err)
	}
	if payload.Object != "list" {
		t.Fatalf("expected list object, got %+v", payload)
	}
	var directModels []struct {
		ID         string
		OwnedBy    string
		Provenance string
	}
	for _, entry := range payload.Data {
		if entry.List {
			if entry.ID == "ghrouter/auto" && (len(entry.Members) != 1 || entry.Members[0] != "oc/good") {
				t.Fatalf("expected auto list to keep verified canonical model only, got %+v", entry)
			}
			continue
		}
		directModels = append(directModels, struct {
			ID         string
			OwnedBy    string
			Provenance string
		}{ID: entry.ID, OwnedBy: entry.OwnedBy, Provenance: entry.Provenance})
		if entry.ID == "oc/bad" {
			t.Fatalf("expected failed/cooldown model to be excluded, got %+v", entry)
		}
	}
	if len(directModels) != 1 {
		t.Fatalf("expected one direct live model, got %+v", directModels)
	}
	entry := directModels[0]
	if entry.ID != "oc/good" || entry.OwnedBy != "opencode" {
		t.Fatalf("unexpected live model entry: %+v", entry)
	}
	if entry.Provenance != "verified" {
		t.Fatalf("expected verified provenance, got %+v", entry)
	}
	snapshot := srv.LiveSnapshot()
	var snapshotDirect []string
	var snapshotLists map[string][]string
	for _, model := range snapshot.Models {
		if model.List {
			if snapshotLists == nil {
				snapshotLists = make(map[string][]string)
			}
			snapshotLists[model.ID] = append([]string(nil), model.Members...)
			continue
		}
		snapshotDirect = append(snapshotDirect, model.ID)
		if model.ID == "oc/bad" {
			t.Fatalf("expected failed/cooldown model to stay excluded from snapshot, got %+v", model)
		}
		if model.ID == "oc/good" && model.Provenance != "verified" {
			t.Fatalf("expected verified provenance, got %+v", model)
		}
	}
	if len(snapshotDirect) != 1 || snapshotDirect[0] != "oc/good" {
		t.Fatalf("expected one direct verified model in snapshot, got %+v", snapshotDirect)
	}
	if members := snapshotLists["ghrouter/auto"]; len(members) != 1 || members[0] != "oc/good" {
		t.Fatalf("expected auto list to keep verified canonical model only, got %+v", members)
	}
	if providers := snapshot.Providers; len(providers) != 1 || len(providers[0].Models) != 1 || providers[0].Models[0] != "oc/good" {
		t.Fatalf("expected only the verified canonical provider model in live providers, got %+v", providers)
	}
}

func TestReloadConfigRefreshesLiveNativeCatalogAfterDetectorSync(t *testing.T) {
	now := time.Now().UTC()
	cfg := &types.Config{
		Providers: []*types.Provider{{
			Name:    "opencode",
			Type:    types.ProviderOpenCode,
			CLIPath: "/bin/true",
			Enabled: true,
			Models:  []string{"oc/stale"},
			ModelInfo: map[string]types.ModelInfo{
				"oc/stale": {Source: "native", VerifiedAt: now, HealthStatus: "healthy"},
			},
		}},
	}
	srv := NewWithConfigPath(cfg, filepath.Join(t.TempDir(), "config.yaml"))

	if got := summaryIDs(srv.LiveSnapshot().Models); !sliceContainsAll(got, "oc/stale") {
		t.Fatalf("expected initial live snapshot to include stale configured model, got %#v", got)
	}

	next := &types.Config{
		Providers: []*types.Provider{{
			Name:    "opencode",
			Type:    types.ProviderOpenCode,
			CLIPath: "/bin/true",
			Enabled: true,
			Models:  []string{"oc/fresh"},
			ModelInfo: map[string]types.ModelInfo{
				"oc/fresh": {Source: "native", VerifiedAt: now.Add(time.Minute), HealthStatus: "healthy"},
			},
		}},
	}
	if err := srv.ReloadConfig(next); err != nil {
		t.Fatalf("reload config: %v", err)
	}

	live := srv.LiveSnapshot()
	ids := summaryIDs(live.Models)
	if !sliceContainsAll(ids, "oc/fresh") {
		t.Fatalf("expected refreshed live catalog to include oc/fresh, got %#v", ids)
	}
	if sliceContainsAny(ids, "oc/stale") {
		t.Fatalf("expected refreshed live catalog to drop stale oc/stale, got %#v", ids)
	}
	if got := srv.catalog.GetModel("oc/fresh"); got == nil || got.Model != "oc/fresh" {
		t.Fatalf("expected catalog to register fresh canonical model, got %+v", got)
	}
	if provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{}); provider != "opencode" || model != "oc/fresh" {
		t.Fatalf("expected reloaded verified model to be routable, got %s/%s", provider, model)
	}
}

func TestAttachProviderAddsLateLocalBrainToLiveRouter(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{{
		Name: "fixture", Type: types.ProviderCustom, Models: []string{"fixture-model"}, Enabled: true,
	}}})

	err := srv.AttachProvider(&types.Provider{
		Name: "local-brain", Type: types.ProviderLocal, BaseURL: "http://127.0.0.1:19090",
		Models: []string{"brain-model"}, Enabled: true,
		ModelInfo: map[string]types.ModelInfo{"brain-model": {Provider: "local-brain", Model: "brain-model", Source: "native", VerifiedAt: time.Now().UTC(), HealthStatus: "healthy"}},
	})
	if err != nil {
		t.Fatalf("expected late provider attach to succeed: %v", err)
	}
	if srv.getProvider("local-brain") == nil {
		t.Fatal("expected attached local brain runner")
	}
	if srv.catalog.GetModel("local-brain/brain-model") == nil {
		t.Fatal("expected attached local brain model in catalog")
	}
	live := srv.LiveSnapshot()
	for _, provider := range live.Providers {
		if provider.Name == "local-brain" {
			if provider.Health != "healthy" {
				t.Fatalf("expected verified local provider health, got %q", provider.Health)
			}
			return
		}
	}
	t.Fatal("expected local-brain provider in live snapshot")
}

func TestExternalLocalBrainDoesNotEnterToolUseList(t *testing.T) {
	srv := New(&types.Config{ModelLists: []types.ModelList{{Name: "ghrouter/tool-use", Kind: "automatic", Strategy: "score"}}})
	provider := &types.Provider{
		Name: "local-brain", Type: types.ProviderLocal, BaseURL: "http://127.0.0.1:8080",
		Models: []string{"mlx-community/gemma-4-e2b-it-4bit"}, Enabled: true,
		ModelInfo: map[string]types.ModelInfo{"mlx-community/gemma-4-e2b-it-4bit": {
			Provider: "local-brain", Model: "mlx-community/gemma-4-e2b-it-4bit", Source: "native",
			HealthStatus: "healthy", VerifiedAt: time.Now().UTC(), ToolUse: false,
		}},
	}
	if err := srv.AttachProvider(provider); err != nil {
		t.Fatal(err)
	}
	for _, list := range srv.LiveSnapshot().ModelLists {
		if list.Name == "ghrouter/tool-use" {
			t.Fatalf("non-tool-capable external local model entered tool-use list: %+v", list)
		}
	}
}

func TestAttachProviderConfiguresLateLocalBrainSelector(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{{
		Name: "fixture", Type: types.ProviderCustom, Models: []string{"fixture-model"}, Enabled: true,
	}}})
	brainURL := "http://127.0.0.1:19091"
	if err := srv.AttachProvider(&types.Provider{
		Name: "local-brain", Type: types.ProviderLocal, BaseURL: brainURL,
		Models: []string{"brain-model"}, Enabled: true,
	}); err != nil {
		t.Fatalf("attach local brain: %v", err)
	}
	if srv.brainURL != brainURL || srv.brainModel != "brain-model" {
		t.Fatalf("expected late local brain selector handoff, got url=%q model=%q", srv.brainURL, srv.brainModel)
	}
	if !srv.brainReadyForSelection() {
		t.Fatal("expected local brain selector to become ready after attach")
	}
}

func TestAttachVerifiedLocalBrainWithConfigPathRemainsVisible(t *testing.T) {
	srv := NewWithConfigPath(&types.Config{Providers: []*types.Provider{{
		Name: "fixture", Type: types.ProviderCustom, Models: []string{"fixture-model"}, Enabled: true,
	}}}, filepath.Join(t.TempDir(), "config.yaml"))
	verifiedAt := time.Now().UTC()
	err := srv.AttachProvider(&types.Provider{
		Name: "local-brain", Type: types.ProviderLocal, BaseURL: "http://127.0.0.1:19090",
		Models: []string{"brain-model"}, Enabled: true,
		ModelInfo: map[string]types.ModelInfo{"brain-model": {
			Provider: "local-brain", Model: "brain-model", Source: "native",
			VerifiedAt: verifiedAt, HealthStatus: "healthy", ToolUse: true,
		}},
	})
	if err != nil {
		t.Fatalf("expected verified local brain attach to succeed: %v", err)
	}
	live := srv.LiveSnapshot()
	for _, provider := range live.Providers {
		if provider.Name != "local-brain" {
			continue
		}
		if len(provider.Models) != 1 || provider.Models[0] != "local-brain/brain-model" {
			t.Fatalf("expected verified local brain model in provider snapshot, got %+v", provider)
		}
		for _, model := range live.Models {
			if model.ID == "local-brain/brain-model" && model.Health != string(health.HealthHealthy) {
				t.Fatalf("expected verified local brain model to remain healthy, got %+v", model)
			}
		}
		return
	}
	t.Fatal("expected local-brain provider in live snapshot")
}

func TestConfiguredVerifiedLocalBrainIsVisibleWithConfigPath(t *testing.T) {
	model := "/Users/tester/.lmstudio/models/mlx-community/gemma-4-e2b-it-4bit"
	srv := NewWithConfigPath(&types.Config{Providers: []*types.Provider{{
		Name: "local-brain", Type: types.ProviderLocal, BaseURL: "http://127.0.0.1:8080",
		Models: []string{model}, Enabled: true,
		ModelInfo: map[string]types.ModelInfo{model: {
			Provider: "local-brain", Model: model, Source: "native",
			VerifiedAt: time.Now().UTC(), HealthStatus: "healthy",
		}},
	}}}, filepath.Join(t.TempDir(), "config.yaml"))
	for _, summary := range srv.FunctionalModelSummaries() {
		if summary.ID == model && summary.Health == "healthy" {
			return
		}
	}
	t.Fatalf("configured verified local model was not visible in functional summaries: %+v", srv.FunctionalModelSummaries())
}

func TestAttachVerifiedLocalBrainUpgradesPlaceholder(t *testing.T) {
	srv := NewWithConfigPath(&types.Config{Providers: []*types.Provider{
		{Name: "local-brain", Type: types.ProviderLocal, Enabled: true},
	}}, filepath.Join(t.TempDir(), "config.yaml"))
	err := srv.AttachProvider(&types.Provider{
		Name: "local-brain", Type: types.ProviderLocal, BaseURL: "http://127.0.0.1:19090",
		Models: []string{"brain-model"}, Enabled: true,
		ModelInfo: map[string]types.ModelInfo{"brain-model": {
			Provider: "local-brain", Model: "brain-model", Source: "native",
			VerifiedAt: time.Now().UTC(), HealthStatus: "healthy", ToolUse: true,
		}},
	})
	if err != nil {
		t.Fatalf("expected verified local brain to upgrade placeholder: %v", err)
	}
	live := srv.LiveSnapshot()
	for _, provider := range live.Providers {
		if provider.Name == "local-brain" && len(provider.Models) == 1 && provider.Models[0] == "local-brain/brain-model" {
			return
		}
	}
	t.Fatalf("expected upgraded local brain model in live snapshot, got %+v", live.Providers)
}

func TestAttachProviderConcurrentWithRouting(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{{
		Name: "fixture", Type: types.ProviderCustom, Models: []string{"fixture-model"}, Enabled: true,
	}}})
	request := &types.OpenAIRequest{Model: "fixture-model", Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			srv.RouteOpenAIRequest(request)
			srv.FunctionalModelSummaries()
			srv.ExplainRequest(request)
			srv.LiveSnapshot()
		}
	}()
	if err := srv.AttachProvider(&types.Provider{
		Name: "local-brain", Type: types.ProviderLocal, BaseURL: "http://127.0.0.1:19090",
		Models: []string{"brain-model"}, Enabled: true,
	}); err != nil {
		t.Fatalf("expected concurrent provider attach to succeed: %v", err)
	}
	<-done
}

func TestModelVerificationSingleFlightSharesConcurrentProbe(t *testing.T) {
	var calls atomic.Int64
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request types.OpenAIRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode probe request: %v", err)
		}
		if request.MaxTokens == nil || *request.MaxTokens != 8 || request.ChatTemplateKwargs["enable_thinking"] != false {
			t.Errorf("expected bounded no-thinking probe, got max_tokens=%v kwargs=%v", request.MaxTokens, request.ChatTemplateKwargs)
		}
		if len(request.Messages) != 1 || request.Messages[0].Content != "Reply exactly OK." {
			t.Errorf("expected minimal probe prompt, got %+v", request.Messages)
		}
		time.Sleep(75 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Yes"}}]}`))
	}))
	defer providerServer.Close()

	srv := New(&types.Config{Providers: []*types.Provider{{
		Name: "local", Type: types.ProviderLocal, BaseURL: providerServer.URL, Models: []string{"model"}, Enabled: true,
		ModelInfo: map[string]types.ModelInfo{"model": {Source: "native", VerifiedAt: time.Now().UTC(), HealthStatus: "healthy"}},
	}}})
	const workers = 8
	results := make(chan ModelTestResult, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- srv.TestModel(context.Background(), "local/model")
		}()
	}
	group.Wait()
	close(results)
	for result := range results {
		if !result.OK || result.Status != "healthy" {
			t.Fatalf("expected shared healthy probe, got %+v", result)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected one provider probe for concurrent callers, got %d", got)
	}
	entry := srv.catalog.GetModel("local/model")
	if entry == nil || entry.LatencyP50 <= 0 || entry.LatencyP95 < entry.LatencyP50 {
		t.Fatalf("expected successful probe latency to feed catalog, got %+v", entry)
	}
}

func TestModelProbeFailurePlacesModelOnCooldown(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"code":"upstream_unavailable"}}`))
	}))
	defer providerServer.Close()

	srv := New(&types.Config{Providers: []*types.Provider{{
		Name: "local", Type: types.ProviderLocal, BaseURL: providerServer.URL, Models: []string{"model"}, Enabled: true,
		ModelInfo: map[string]types.ModelInfo{"model": {Source: "native", VerifiedAt: time.Now().UTC(), HealthStatus: "healthy"}},
	}}})
	result := srv.TestModel(context.Background(), "local/model")
	if result.OK || result.Status != "failed" || result.CooldownUntil.IsZero() {
		t.Fatalf("expected failed probe with cooldown, got %+v", result)
	}
	entry := srv.catalog.GetModel("local/model")
	if entry == nil || entry.HealthStatus != health.HealthCooldown || entry.CooldownUntil.IsZero() {
		t.Fatalf("expected failed model to enter cooldown, got %+v", entry)
	}
}

func TestExplainVirtualRouteUsesTheSameCandidatesAsExecution(t *testing.T) {
	verifiedAt := time.Now().UTC()
	providers := make([]*types.Provider, 0, 3)
	for _, name := range []string{"alpha", "beta", "gamma"} {
		model := name + "/model"
		providers = append(providers, &types.Provider{
			Name: name, Type: types.ProviderCustom, Models: []string{model}, Enabled: true,
			ModelInfo: map[string]types.ModelInfo{model: {Provider: name, Model: model, HealthStatus: "healthy", VerifiedAt: verifiedAt}},
		})
	}
	srv := NewWithConfigPath(&types.Config{
		Providers:  providers,
		ModelLists: []types.ModelList{{Name: "ghrouter/restricted", Kind: "provider", Strategy: "round-robin", Models: []string{"alpha/model", "beta/model"}}},
	}, "/tmp/explain-virtual-route.yaml")
	req := &types.OpenAIRequest{Model: "ghrouter/restricted", Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}}
	provider, model := srv.RouteOpenAIRequest(req)
	actual := srv.routeCandidates(req.Model, provider, model, req)
	explanation := srv.ExplainRequest(req)
	if len(explanation.Candidates) != len(actual) {
		t.Fatalf("expected explain candidates to match execution, got explain=%d actual=%d", len(explanation.Candidates), len(actual))
	}
	for _, candidate := range explanation.Candidates {
		if candidate.ID == "gamma/model" {
			t.Fatalf("explain exposed candidate outside virtual route: %+v", candidate)
		}
	}

	autoReq := &types.OpenAIRequest{Model: "ghrouter/auto", Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}}
	srv.cfg.ModelLists = append(srv.cfg.ModelLists, types.ModelList{Name: "ghrouter/auto", Kind: "automatic", Strategy: "score"})
	srv.RouteOpenAIRequest(autoReq)
	autoExplanation := srv.ExplainRequest(autoReq)
	if autoReq.SelectionStage == "" || autoExplanation.SelectionSource != autoReq.SelectionStage {
		t.Fatalf("expected explain selection source %q to match automatic execution stage %q", autoExplanation.SelectionSource, autoReq.SelectionStage)
	}
}

func TestReloadConfigReRegistersLiveCatalogAndKeepsCooldownState(t *testing.T) {
	now := time.Now().UTC()
	cfg := &types.Config{
		Providers: []*types.Provider{
			{
				Name:    "opencode",
				Type:    types.ProviderOpenCode,
				CLIPath: "/bin/true",
				Enabled: true,
				ModelInfo: map[string]types.ModelInfo{
					"oc/old": {
						Source:       "configured",
						VerifiedAt:   now,
						HealthStatus: "healthy",
					},
					"oc/persist": {
						Source:        "configured",
						VerifiedAt:    now,
						HealthStatus:  "failed",
						CooldownUntil: now.Add(time.Hour),
					},
				},
			},
		},
	}
	srv := NewWithConfigPath(cfg, filepath.Join(t.TempDir(), "config.yaml"))

	initial := srv.LiveSnapshot()
	if got := summaryIDs(initial.Models); !sliceContainsAll(got, "oc/old") || sliceContainsAny(got, "oc/new") {
		t.Fatalf("unexpected initial live models: %#v", got)
	}
	if got := catalogIDs(srv.catalog.GetAllModels()); !sliceContainsAll(got, "oc/old") || !sliceContainsAll(got, "oc/persist") {
		t.Fatalf("unexpected initial catalog models: %#v", got)
	}

	next := &types.Config{
		Providers: []*types.Provider{
			{
				Name:    "opencode",
				Type:    types.ProviderOpenCode,
				CLIPath: "/bin/true",
				Enabled: true,
				ModelInfo: map[string]types.ModelInfo{
					"oc/new": {
						Source:       "configured",
						VerifiedAt:   now.Add(time.Minute),
						HealthStatus: "healthy",
					},
					"oc/persist": {
						Source:       "configured",
						VerifiedAt:   now.Add(time.Minute),
						HealthStatus: "healthy",
					},
				},
			},
		},
	}
	if err := srv.ReloadConfig(next); err != nil {
		t.Fatalf("reload config: %v", err)
	}

	live := srv.LiveSnapshot()
	ids := summaryIDs(live.Models)
	if !sliceContainsAll(ids, "oc/new") {
		t.Fatalf("expected reloaded inventory to include oc/new, got %#v", ids)
	}
	if sliceContainsAny(ids, "oc/old") {
		t.Fatalf("expected stale oc/old to be removed after reload, got %#v", ids)
	}
	if sliceContainsAny(ids, "oc/persist") {
		t.Fatalf("expected fail-closed oc/persist to stay excluded, got %#v", ids)
	}
	if got := catalogIDs(srv.catalog.GetAllModels()); !sliceContainsAll(got, "oc/new", "oc/persist") || sliceContainsAny(got, "oc/old") {
		t.Fatalf("unexpected catalog models after reload: %#v", got)
	}
	persisted := srv.catalog.GetModel("oc/persist")
	if persisted == nil || persisted.HealthStatus != health.HealthCooldown || persisted.CooldownUntil.Before(time.Now()) {
		t.Fatalf("expected oc/persist cooldown to survive reload, got %+v", persisted)
	}
}

func TestRouteModelReturnsCanonicalID(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{
			{
				Name:    "claude-code",
				Type:    types.ProviderClaudeCode,
				Enabled: true,
				Models:  []string{"claude-sonnet-5"},
			},
		},
	})

	provider, model := srv.RouteModel("cc/claude-sonnet-5")
	if provider != "claude-code" {
		t.Fatalf("expected claude-code provider, got %q", provider)
	}
	if model != "cc/claude-sonnet-5" {
		t.Fatalf("expected canonical model ID, got %q", model)
	}
}

func TestModelVerificationResolvesCanonicalMetadataForUnprefixedModel(t *testing.T) {
	verifiedAt := time.Now().UTC()
	cfg := &types.Config{Providers: []*types.Provider{{
		Name:    "codex",
		Type:    types.ProviderCodex,
		Enabled: true,
		Models:  []string{"gpt-5.4-mini"},
		ModelInfo: map[string]types.ModelInfo{
			"cx/gpt-5.4-mini": {Source: "native", HealthStatus: "healthy", VerifiedAt: verifiedAt},
		},
	}}}
	srv := NewWithConfigPath(cfg, filepath.Join(t.TempDir(), "config.yaml"))
	if !srv.modelVerified("codex", "gpt-5.4-mini") {
		t.Fatal("expected canonical metadata to verify an unprefixed configured model")
	}
	if got := srv.catalog.GetModel("cx/gpt-5.4-mini"); got == nil || got.HealthStatus != health.HealthHealthy {
		t.Fatalf("expected canonical model to remain healthy, got %+v", got)
	}
}

func TestHealthCheckRejectsEmptyModelList(t *testing.T) {
	runner := providers.NewProviderRunner(&types.Provider{
		Name:    "alpha",
		Enabled: true,
		Models:  nil,
	})

	err := runner.HealthCheck(httptest.NewRequest(http.MethodGet, "/", nil).Context())
	if err == nil {
		t.Fatal("expected error for provider with no models")
	}
	if !strings.Contains(err.Error(), "no configured models") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLiveSnapshotIncludesAccountStatus(t *testing.T) {
	cfg := &types.Config{
		Providers: []*types.Provider{
			{
				Name:    "anthropic",
				Enabled: true,
				Models:  []string{"model-a"},
				AuthConfig: map[string]string{
					"account_json": `{"plan":"pro","balance":3.5,"currency":"USD","source":"fixture","available":true,"healthy":true}`,
				},
			},
		},
	}

	srv := New(cfg)
	snapshot := srv.LiveSnapshot()
	if len(snapshot.Providers) != 1 {
		t.Fatalf("expected one provider, got %+v", snapshot.Providers)
	}
	account := snapshot.Providers[0].Account
	if account.Plan != "pro" {
		t.Fatalf("expected account plan pro, got %+v", snapshot.Providers[0])
	}
	if account.Balance == nil || *account.Balance != 3.5 {
		t.Fatalf("expected balance 3.5, got %+v", snapshot.Providers[0])
	}
	if account.Currency != "USD" {
		t.Fatalf("expected currency USD, got %+v", snapshot.Providers[0])
	}
	if account.Source != "fixture" {
		t.Fatalf("expected source fixture, got %+v", snapshot.Providers[0])
	}
}

func TestLiveSnapshotDoesNotLabelHTTPProviderAsMissingCLI(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{{
		Name: "nvidia", Type: types.ProviderNVIDIA, BaseURL: "http://127.0.0.1:1",
		Models: []string{"nv/model"}, Enabled: true,
	}}})

	snapshot := srv.LiveSnapshot()
	if len(snapshot.Providers) != 1 {
		t.Fatalf("expected one provider, got %+v", snapshot.Providers)
	}
	provider := snapshot.Providers[0]
	if provider.Auth == "missing CLI on PATH" || provider.Health == "unavailable" {
		t.Fatalf("HTTP provider was mislabeled as CLI-only: %+v", provider)
	}
}

func TestHealthSnapshotReportsCircuitBeforePassiveHealthSample(t *testing.T) {
	tmpDir := t.TempDir()
	cliPath := filepath.Join(tmpDir, "failing-cli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := New(&types.Config{Providers: []*types.Provider{{Name: "failing", Type: types.ProviderCustom, CLIPath: cliPath, Models: []string{"model"}, Enabled: true}}})
	srv.providers["failing"].SetCircuitPolicy(providers.CircuitPolicy{Enabled: true, FailureThreshold: 1, OpenDuration: time.Hour})
	events, errs := srv.providers["failing"].Invoke(context.Background(), &types.OpenAIRequest{Model: "model"})
	for events != nil || errs != nil {
		select {
		case _, ok := <-events:
			if !ok {
				events = nil
			}
		case _, ok := <-errs:
			if !ok {
				errs = nil
			}
		}
	}
	summary := srv.healthSnapshot()
	if summary.CircuitOpen != 1 || summary.Providers["failing"].Status != "circuit_open" {
		t.Fatalf("expected circuit state in health snapshot, got %+v", summary)
	}
}

func TestHealthStateOmitsUnknownTimestamp(t *testing.T) {
	data, err := json.Marshal(HealthState{Status: string(health.HealthUnknown)})
	if err != nil {
		t.Fatalf("marshal health state: %v", err)
	}
	if strings.Contains(string(data), `"timestamp"`) {
		t.Fatalf("unknown health state must not claim an observation timestamp: %s", data)
	}

	observedAt := time.Now().UTC()
	data, err = json.Marshal(HealthState{Status: string(health.HealthHealthy), Timestamp: observedAt})
	if err != nil {
		t.Fatalf("marshal observed health state: %v", err)
	}
	if !strings.Contains(string(data), `"timestamp"`) {
		t.Fatalf("observed health state must preserve its timestamp: %s", data)
	}
}

func TestLiveJSONOmitsUnobservedTimes(t *testing.T) {
	srv := New(&types.Config{
		Providers: []*types.Provider{{
			Name:    "fixture",
			Type:    types.ProviderCustom,
			Models:  []string{"fixture-model"},
			Enabled: true,
			ModelInfo: map[string]types.ModelInfo{
				"fixture-model": {Source: "configured", HealthStatus: "healthy", VerifiedAt: time.Now().UTC()},
			},
		}},
	})
	data, err := json.Marshal(srv.LiveSnapshot())
	if err != nil {
		t.Fatalf("marshal live snapshot: %v", err)
	}
	if strings.Contains(string(data), `0001-01-01T00:00:00Z`) {
		t.Fatalf("live snapshot must not publish unobserved timestamps: %s", data)
	}
}

func TestLiveSnapshotBuildsRoutingGraphWithModelNodes(t *testing.T) {
	srv := New(&types.Config{
		Providers:  []*types.Provider{{Name: "opencode", Type: types.ProviderOpenCode, Models: []string{"oc/free"}, Enabled: true}},
		ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"oc/free"}, Strategy: "score"}},
		Routes:     []*types.Route{{Pattern: "auto/*", Provider: "auto", Mode: "auto"}},
	})
	snapshot := srv.LiveSnapshot()
	nodes := make(map[string]RoutingGraphNode)
	for _, node := range snapshot.Graph.Nodes {
		nodes[node.ID] = node
	}
	if nodes["brain"].Kind != "brain" || nodes["provider/opencode"].Kind != "provider" {
		t.Fatalf("expected brain and provider graph nodes, got %+v", snapshot.Graph.Nodes)
	}
	model, ok := nodes["model/oc/free"]
	if !ok || model.Kind != "model" || model.Provider != "opencode" {
		t.Fatalf("expected canonical model graph node, got %+v", model)
	}
	if _, ok := nodes["list/ghrouter/auto"]; !ok {
		t.Fatalf("expected virtual list graph node, got %+v", snapshot.Graph.Nodes)
	}
	foundModelEdge := false
	for _, edge := range snapshot.Graph.Edges {
		if edge.From == "brain" && edge.To == "model/oc/free" && edge.Relation == "route" {
			foundModelEdge = true
			break
		}
	}
	if !foundModelEdge {
		t.Fatalf("expected brain-to-model route edge, got %+v", snapshot.Graph.Edges)
	}
	legend := make(map[string]string)
	for _, item := range snapshot.Graph.Legend {
		legend[item.Status] = item.Color
	}
	if legend["available"] != "green" || legend["unavailable"] != "red" || legend["cooldown"] != "blue" {
		t.Fatalf("expected availability legend colors, got %+v", legend)
	}
}

func TestLiveSnapshotJSONIncludesAccountStatus(t *testing.T) {
	cfg := &types.Config{
		Providers: []*types.Provider{
			{
				Name:    "openai",
				Enabled: true,
				Models:  []string{"model-b"},
				AuthConfig: map[string]string{
					"account_json": `{"plan":"team","balance":1.25,"currency":"USD","source":"fixture","available":true,"healthy":true}`,
				},
			},
		},
	}

	srv := New(cfg)
	payload, err := json.Marshal(srv.LiveSnapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if !strings.Contains(string(payload), `"account"`) {
		t.Fatalf("expected account field in live snapshot JSON, got %s", string(payload))
	}
}

func summaryIDs(models []ModelSummary) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func catalogIDs(models []*catalog.ModelEntry) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		ids = append(ids, model.ID)
	}
	return ids
}

func sliceContainsAll(values []string, wants ...string) bool {
	for _, want := range wants {
		found := false
		for _, value := range values {
			if value == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sliceContainsAny(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestHandleLiveRespondsOnV1Path(t *testing.T) {
	srv := New(&types.Config{})
	req := httptest.NewRequest(http.MethodGet, "/v1/live", nil)
	rec := httptest.NewRecorder()

	srv.handleLive(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"snapshot"`) {
		t.Fatalf("expected live response snapshot, got %s", rec.Body.String())
	}
}

func TestApplyModelVerificationPersistsProviderCapacityFailure(t *testing.T) {
	reset := time.Now().Add(time.Hour)
	srv := New(&types.Config{Providers: []*types.Provider{
		{Name: "cursor", Type: types.ProviderCursor, Models: []string{"cu/auto", "cu/composer-2.5"}, Enabled: true},
	}})
	srv.applyModelVerification([]ModelTestResult{{
		Provider:      "cursor",
		Model:         "cu/auto",
		Status:        "failed",
		Error:         "provider capacity limit reached",
		CooldownUntil: reset,
	}}, time.Now())
	for _, model := range []string{"cu/auto", "cu/composer-2.5"} {
		info := srv.cfg.Providers[0].ModelInfo[model]
		if info.HealthStatus != "failed" || !info.CooldownUntil.Equal(reset) || !info.VerifiedAt.IsZero() {
			t.Fatalf("expected provider-wide capacity state for %s, got %+v", model, info)
		}
	}
}

func TestFailedModelVerificationDoesNotCreateSuccessEvidence(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{{
		Name: "fixture", Type: types.ProviderCustom, Models: []string{"model"}, Enabled: true,
	}}})
	srv.applyModelVerification([]ModelTestResult{{Provider: "fixture", Model: "model", Status: "failed", Error: "probe failed"}}, time.Now().UTC())
	info := srv.cfg.Providers[0].ModelInfo["model"]
	if !info.VerifiedAt.IsZero() || info.VerificationError != "probe failed" {
		t.Fatalf("failed probe created invalid success evidence: %+v", info)
	}
	verifiedAt := time.Now().UTC()
	srv.applyModelVerification([]ModelTestResult{{Provider: "fixture", Model: "model", Status: "healthy", Error: "stale error"}}, verifiedAt)
	info = srv.cfg.Providers[0].ModelInfo["model"]
	if !info.VerifiedAt.Equal(verifiedAt) || info.VerificationError != "" {
		t.Fatalf("successful probe did not replace verification evidence: %+v", info)
	}
}

func TestApplyModelVerificationUpdatesCanonicalMetadataKey(t *testing.T) {
	cfg := &types.Config{Providers: []*types.Provider{{
		Name:    "codex",
		Type:    types.ProviderCodex,
		Enabled: true,
		Models:  []string{"gpt-5.4-mini"},
		ModelInfo: map[string]types.ModelInfo{
			"cx/gpt-5.4-mini": {Source: "native"},
		},
	}}}
	srv := NewWithConfigPath(cfg, filepath.Join(t.TempDir(), "config.yaml"))
	verifiedAt := time.Now().UTC()
	srv.applyModelVerification([]ModelTestResult{{Provider: "codex", Model: "gpt-5.4-mini", Status: "healthy"}}, verifiedAt)
	info := srv.cfg.Providers[0].ModelInfo["cx/gpt-5.4-mini"]
	if !info.VerifiedAt.Equal(verifiedAt) || info.HealthStatus != "healthy" {
		t.Fatalf("expected canonical metadata key to be updated, got %+v", info)
	}
	if _, ok := srv.cfg.Providers[0].ModelInfo["gpt-5.4-mini"]; ok {
		t.Fatal("verification should not create a duplicate unprefixed metadata key")
	}
}

func TestApplyModelVerificationAddsNewHealthyModelToProvider(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{{
		Name: "nvidia", Type: types.ProviderNVIDIA, Enabled: true,
		Models:    []string{"nv/configured"},
		ModelInfo: map[string]types.ModelInfo{"nv/discovered": {Source: "nvidia_api"}},
	}}})

	srv.applyModelVerification([]ModelTestResult{{
		Provider: "nvidia", Model: "nv/discovered", Status: "healthy",
	}}, time.Now().UTC())

	found := false
	for _, model := range srv.cfg.Providers[0].Models {
		if model == "nv/discovered" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected verified discovered model in provider models, got %v", srv.cfg.Providers[0].Models)
	}
}

func TestNativeCodexModelsAdvertiseToolUse(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{{
		Name:    "codex",
		Type:    types.ProviderCodex,
		Models:  []string{"cx/gpt-5.4-mini"},
		Enabled: true,
		ModelInfo: map[string]types.ModelInfo{
			"cx/gpt-5.4-mini": {Source: "native", HealthStatus: "healthy", VerifiedAt: time.Now().UTC()},
		},
	}}})
	entry := srv.catalog.GetModel("cx/gpt-5.4-mini")
	if entry == nil || !entry.ToolUse {
		t.Fatalf("expected native Codex model to advertise tool use, got %+v", entry)
	}
}
