package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ghrouter/internal/account"
	"ghrouter/internal/catalog"
	"ghrouter/internal/health"
	"ghrouter/internal/resourcegov"
	"ghrouter/internal/types"
)

func TestProfileRequestClassifiesToolCodeRequest(t *testing.T) {
	profile := ProfileRequest(&types.OpenAIRequest{
		ReasoningEffort: "high",
		Tools:           []types.OpenAITool{{Type: "function"}},
		Messages:        []types.OpenAIMessage{{Role: "user", Content: "fix this Go handler and add a regression test"}},
	})
	if profile.Intent != IntentCode || !profile.NeedsTools || profile.ReasoningEffort != "high" {
		t.Fatalf("unexpected code profile: %+v", profile)
	}
	if profile.Complexity != ComplexityHigh || profile.Modality != ModalityText {
		t.Fatalf("expected high-complexity text profile, got %+v", profile)
	}
}

func TestProfileRequestClassifiesVisionAndEconomyRequest(t *testing.T) {
	profile := ProfileRequest(&types.OpenAIRequest{
		Messages: []types.OpenAIMessage{{Role: "user", Content: []interface{}{
			map[string]interface{}{"type": "text", "text": "summarize this image using the cheapest option"},
			map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": "https://example.invalid/image"}},
		}}},
	})
	if profile.Intent != IntentVision || !profile.NeedsVision || profile.CostClass != CostClassEconomy {
		t.Fatalf("unexpected vision profile: %+v", profile)
	}
	if profile.Complexity != ComplexityStandard || profile.Modality != ModalityImage {
		t.Fatalf("expected standard image profile, got %+v", profile)
	}
}

func TestProfileRequestMarksCriticalWork(t *testing.T) {
	profile := ProfileRequest(&types.OpenAIRequest{
		ReasoningEffort: "max",
		Messages:        []types.OpenAIMessage{{Role: "user", Content: "handle this production outage and security incident"}},
	})
	if profile.Complexity != ComplexityCritical {
		t.Fatalf("expected critical profile, got %+v", profile)
	}
}

func TestProfileRequestClassifiesPortugueseRoutingIntent(t *testing.T) {
	profile := ProfileRequest(&types.OpenAIRequest{Messages: []types.OpenAIMessage{{Role: "user", Content: "analise a arquitetura e planeje uma migracao usando a opcao gratuita"}}})
	if profile.Intent != IntentReasoning || profile.CostClass != CostClassEconomy || profile.Complexity != ComplexityHigh {
		t.Fatalf("expected Portuguese reasoning/economy profile, got %+v", profile)
	}
	if slot := slotForRequest(&types.OpenAIRequest{Messages: []types.OpenAIMessage{{Role: "user", Content: "depure este codigo e execute o teste"}}}); slot != catalog.SlotFastCode {
		t.Fatalf("expected Portuguese coding request to use fast-code slot, got %q", slot)
	}
}

func TestRequestModelEligibilityRequiresCapabilities(t *testing.T) {
	profile := ProfileRequest(&types.OpenAIRequest{
		Tools:    []types.OpenAITool{{Type: "function"}},
		Messages: []types.OpenAIMessage{{Role: "user", Content: "inspect this screenshot"}},
	})
	if requestModelEligible(&catalog.ModelEntry{ToolUse: true, Vision: false}, profile) {
		t.Fatal("expected a model without vision to be ineligible")
	}
	if !requestModelEligible(&catalog.ModelEntry{ToolUse: true, Vision: true}, profile) {
		t.Fatal("expected a model with required capabilities to be eligible")
	}
}

func TestRequestModelEligibilityRequiresDeclaredReasoningEffort(t *testing.T) {
	profile := ProfileRequest(&types.OpenAIRequest{ReasoningEffort: "xhigh"})
	if requestModelEligible(&catalog.ModelEntry{Effort: []string{"low", "high"}}, profile) {
		t.Fatal("expected model without requested effort to be ineligible")
	}
	if !requestModelEligible(&catalog.ModelEntry{Effort: []string{"high", "xhigh"}}, profile) {
		t.Fatal("expected model with requested effort to be eligible")
	}
	if !requestModelEligible(&catalog.ModelEntry{}, profile) {
		t.Fatal("expected missing effort metadata to remain unknown rather than block routing")
	}
	none := ProfileRequest(&types.OpenAIRequest{ReasoningEffort: "none"})
	if !requestModelEligible(&catalog.ModelEntry{Effort: []string{"low"}}, none) {
		t.Fatal("expected reasoning_effort=none not to require a declared effort")
	}
}

func TestRequestModelEligibilityRespectsDeclaredOutputCapacity(t *testing.T) {
	profile := ProfileRequest(&types.OpenAIRequest{
		MaxTokens: func() *int { value := 4096; return &value }(),
		Messages:  []types.OpenAIMessage{{Role: "user", Content: "write a short answer"}},
	})
	if requestModelEligible(&catalog.ModelEntry{MaxOutput: 2048, ContextWindow: 128000}, profile) {
		t.Fatal("expected model with insufficient declared max output to be ineligible")
	}
	if !requestModelEligible(&catalog.ModelEntry{MaxOutput: 8192, ContextWindow: 128000}, profile) {
		t.Fatal("expected model with sufficient declared max output to be eligible")
	}
	if !requestModelEligible(&catalog.ModelEntry{}, profile) {
		t.Fatal("expected unknown context metadata not to block output routing")
	}
	largePrompt := ProfileRequest(&types.OpenAIRequest{
		MaxTokens: func() *int { value := 4096; return &value }(),
		Messages:  []types.OpenAIMessage{{Role: "user", Content: strings.Repeat("token ", 10000)}},
	})
	if requestModelEligible(&catalog.ModelEntry{ContextWindow: largePrompt.EstimatedTokens + 1, MaxOutput: 4096}, largePrompt) {
		t.Fatal("expected combined input and output capacity to be enforced")
	}
}

func TestQuotaScoreBlocksExplicitResetWindow(t *testing.T) {
	resetAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	provider := &types.Provider{AuthConfig: map[string]string{
		"account_json": `{"source":"quota","available":true,"healthy":true,"balance":0,"reset_at":"` + resetAt + `"}`,
	}}
	status := account.Load(provider)
	if got := quotaScore(status); got >= 0 {
		t.Fatalf("expected exhausted quota to be penalized, got %v", got)
	}
}

func TestModelPolicyEnforcesCostAndDiscoveryFreshness(t *testing.T) {
	now := time.Now().UTC()
	fresh := &types.Provider{
		Name: "fresh", Type: types.ProviderCustom, CLIPath: "/bin/true", Enabled: true,
		Models: []string{"fresh-model"}, ModelInfo: map[string]types.ModelInfo{
			"fresh-model": {Source: "native", DiscoveredAt: now, VerifiedAt: now, HealthStatus: "healthy", TokenCost: 5},
		},
	}
	expensive := &types.Provider{
		Name: "expensive", Type: types.ProviderCustom, CLIPath: "/bin/true", Enabled: true,
		Models: []string{"expensive-model"}, ModelInfo: map[string]types.ModelInfo{
			"expensive-model": {Source: "native", DiscoveredAt: now, VerifiedAt: now, HealthStatus: "healthy", TokenCost: 50},
		},
	}
	stale := &types.Provider{
		Name: "stale", Type: types.ProviderCustom, CLIPath: "/bin/true", Enabled: true,
		Models: []string{"stale-model"}, ModelInfo: map[string]types.ModelInfo{
			"stale-model": {Source: "native", DiscoveredAt: now.Add(-2 * time.Hour), VerifiedAt: now, HealthStatus: "healthy", TokenCost: 5},
		},
	}
	srv := New(&types.Config{
		ModelPolicy: types.ModelPolicy{MaxCostMicros: 10, MaxDiscoveryAge: time.Hour},
		Providers:   []*types.Provider{fresh, expensive, stale},
	})

	provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{})
	if provider != "fresh" || model != "fresh-model" {
		t.Fatalf("expected fresh affordable model, got %s/%s", provider, model)
	}
	if provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "expensive/expensive-model"}); provider == "expensive" || model == "expensive-model" {
		t.Fatalf("expected expensive explicit route to fall back, got %s/%s", provider, model)
	}
	if provider, model := srv.RouteOpenAIRequest(&types.OpenAIRequest{Model: "stale/stale-model"}); provider == "stale" || model == "stale-model" {
		t.Fatalf("expected stale explicit route to fall back, got %s/%s", provider, model)
	}
}

func TestProfileRequestBuildsGraphForToolTask(t *testing.T) {
	profile := ProfileRequest(&types.OpenAIRequest{
		Tools:    []types.OpenAITool{{Type: "function"}},
		Messages: []types.OpenAIMessage{{Role: "user", Content: "fix the handler and add a test"}},
	})
	if len(profile.Graph.Stages) != 3 || profile.Graph.Stages[0] != GraphPlan || profile.Graph.Stages[2] != GraphVerify {
		t.Fatalf("expected plan, implement, verify graph, got %+v", profile.Graph)
	}
	if profile.Graph.Parallel {
		t.Fatal("tool execution graph must preserve stage order")
	}
	if len(profile.Graph.Nodes) != 3 || len(profile.Graph.Edges) != 2 {
		t.Fatalf("expected three graph nodes and two edges, got %+v", profile.Graph)
	}
	if profile.Graph.Edges[1].From != string(GraphImplement) || profile.Graph.Edges[1].To != string(GraphVerify) {
		t.Fatalf("expected implement to precede verify, got %+v", profile.Graph.Edges)
	}
}

func TestGraphStagesFollowRequestIntent(t *testing.T) {
	profile := ProfileRequest(&types.OpenAIRequest{
		Tools:    []types.OpenAITool{{Type: "function"}},
		Messages: []types.OpenAIMessage{{Role: "user", Content: "implement and verify this Go change"}},
	})
	if got := graphStageForIndex(0, profile); got != GraphPlan {
		t.Fatalf("expected code graph to start with plan, got %q", got)
	}
	if got := graphStageForIndex(1, profile); got != GraphImplement {
		t.Fatalf("expected code graph second stage to implement, got %q", got)
	}
	content, ok := graphStageRequest(&types.OpenAIRequest{}, GraphVerify).Messages[0].Content.(string)
	if !ok || !strings.Contains(content, "Verify") {
		t.Fatal("expected verify stage to receive a verification instruction")
	}
}

func TestLocalBrainSelectsOnlyAnAllowedCatalogModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode local brain request: %v", err)
		}
		if request["stream"] != false || request["max_tokens"] != float64(32) {
			t.Errorf("expected bounded non-stream selector request, got stream=%#v max_tokens=%#v", request["stream"], request["max_tokens"])
		}
		kwargs, ok := request["chat_template_kwargs"].(map[string]any)
		if !ok || kwargs["enable_thinking"] != false {
			t.Errorf("expected selector thinking disabled, got %#v", request["chat_template_kwargs"])
		}
		messages, ok := request["messages"].([]any)
		if !ok || len(messages) < 2 {
			t.Errorf("expected selector messages, got %#v", request["messages"])
		} else if userMessage, ok := messages[1].(map[string]any); !ok {
			t.Errorf("expected selector user message, got %#v", messages[1])
		} else if content, ok := userMessage["content"].(string); !ok {
			t.Errorf("expected selector JSON content, got %#v", userMessage["content"])
		} else {
			var input struct {
				Candidates []map[string]any `json:"candidates"`
			}
			if err := json.Unmarshal([]byte(content), &input); err != nil || len(input.Candidates) != 2 {
				t.Errorf("expected two selector candidates, got %s", content)
			} else if input.Candidates[0]["health"] == nil || input.Candidates[0]["error_rate"] == nil || input.Candidates[0]["quota_score"] == nil {
				t.Errorf("expected redacted health evidence in candidate, got %#v", input.Candidates[0])
			} else if input.Candidates[0]["preferred"] != true {
				t.Errorf("expected configured preferred candidate, got %#v", input.Candidates[0])
			} else if _, present := input.Candidates[0]["cooldown_until"]; present {
				t.Errorf("candidate without cooldown must not claim cooldown evidence, got %#v", input.Candidates[0])
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"model":"oc/free","reason":"fast and free"}`}}}})
	}))
	defer server.Close()

	router := &Server{cfg: &types.Config{ModelPolicy: types.ModelPolicy{Preferred: []string{"oc/*"}}}, brainURL: server.URL, brainModel: "local-brain"}
	request := &types.OpenAIRequest{Messages: []types.OpenAIMessage{{Role: "user", Content: "answer quickly"}}}
	candidates := []*catalog.ModelEntry{
		{Provider: "opencode", Model: "oc/free", ID: "oc/free", HealthStatus: "healthy"},
		{Provider: "codex", Model: "cx/paid", ID: "cx/paid", HealthStatus: "healthy"},
	}
	selected := router.selectWithLocalBrain(request, candidates)
	if selected == nil || selected.ID != "oc/free" {
		t.Fatalf("expected local brain to select allowed model, got %#v", selected)
	}
	if request.SelectionReason != "fast and free" {
		t.Fatalf("expected Brain reason to remain observable, got %q", request.SelectionReason)
	}
}

func TestLocalBrainAdmissionLimitsConcurrentSelectors(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startOnce.Do(func() { close(started) })
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"model":"oc/free"}`}}}})
	}))
	defer server.Close()

	router := &Server{
		cfg:            &types.Config{},
		brainURL:       server.URL,
		brainModel:     "local-brain",
		brainAdmission: resourcegov.New(resourcegov.Config{BrainMaxInFlight: 1}, nil, nil),
	}
	candidates := []*catalog.ModelEntry{
		{Provider: "opencode", Model: "oc/free", ID: "oc/free", HealthStatus: health.HealthHealthy},
		{Provider: "codex", Model: "cx/paid", ID: "cx/paid", HealthStatus: health.HealthHealthy},
	}
	firstDone := make(chan *catalog.ModelEntry, 1)
	go func() {
		firstDone <- router.selectWithLocalBrain(&types.OpenAIRequest{Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}}, candidates)
	}()
	<-started
	secondDone := make(chan *catalog.ModelEntry, 1)
	go func() {
		secondDone <- router.selectWithLocalBrain(&types.OpenAIRequest{Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}}, candidates)
	}()
	select {
	case <-secondDone:
		t.Fatal("second selector should wait for the Brain admission slot")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if first := <-firstDone; first == nil || first.ID != "oc/free" {
		t.Fatalf("first selector should complete normally after admission release, got %#v", first)
	}
	if second := <-secondDone; second == nil || second.ID != "oc/free" {
		t.Fatalf("second selector should complete after admission release, got %#v", second)
	}
}

func TestLocalBrainSelectorBoundsCandidatePanel(t *testing.T) {
	var candidateCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode selector request: %v", err)
			return
		}
		messages, _ := request["messages"].([]any)
		user, _ := messages[1].(map[string]any)
		content, _ := user["content"].(string)
		var input struct {
			Candidates []map[string]any `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(content), &input); err != nil {
			t.Errorf("decode selector candidates: %v", err)
			return
		}
		candidateCount = len(input.Candidates)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"model":"oc/model-0"}`}}}})
	}))
	defer server.Close()

	router := &Server{cfg: &types.Config{}, brainURL: server.URL, brainModel: "local-brain"}
	candidates := make([]*catalog.ModelEntry, 40)
	for i := range candidates {
		candidates[i] = &catalog.ModelEntry{Provider: "opencode", Model: fmt.Sprintf("oc/model-%d", i), ID: fmt.Sprintf("oc/model-%d", i), HealthStatus: health.HealthHealthy}
	}
	selected := router.selectWithLocalBrain(&types.OpenAIRequest{Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}}, candidates)
	if selected == nil || selected.ID != "oc/model-0" {
		t.Fatalf("expected bounded selector to choose allowed model, got %+v", selected)
	}
	if candidateCount != maxBrainSelectionCandidates {
		t.Fatalf("expected selector panel capped at %d candidates, got %d", maxBrainSelectionCandidates, candidateCount)
	}
}

func TestLocalBrainRespectsPreferredPolicyForSimpleWork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"model\":\"cx/paid\",\"reason\":\"strong\"}"}}]}`))
	}))
	defer server.Close()
	router := &Server{cfg: &types.Config{ModelPolicy: types.ModelPolicy{Preferred: []string{"oc/*"}}}, brainURL: server.URL, brainModel: "local-brain"}
	candidates := []*catalog.ModelEntry{
		{Provider: "opencode", Model: "oc/free", ID: "oc/free", CostTier: catalog.CostFree, HealthStatus: health.HealthHealthy},
		{Provider: "codex", Model: "cx/paid", ID: "cx/paid", CostTier: catalog.CostUnknown, HealthStatus: health.HealthHealthy},
	}
	if selected := router.selectWithLocalBrain(&types.OpenAIRequest{Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}}, candidates); selected != nil {
		t.Fatalf("expected preferred policy to override non-preferred simple selection, got %#v", selected)
	}
	request := &types.OpenAIRequest{Messages: []types.OpenAIMessage{{Role: "user", Content: "analyze the architecture tradeoffs and plan the migration"}}}
	selected := router.selectWithLocalBrain(request, candidates)
	if selected == nil || selected.ID != "cx/paid" {
		t.Fatalf("expected high-complexity selection to preserve capability choice, got %#v", selected)
	}
}

func TestLocalBrainSelectionFailsClosedToDeterministicFallback(t *testing.T) {
	router := &Server{brainURL: "http://127.0.0.1:1", brainModel: "local-brain"}
	candidates := []*catalog.ModelEntry{
		{Provider: "opencode", Model: "oc/free", ID: "oc/free"},
		{Provider: "codex", Model: "cx/paid", ID: "cx/paid"},
	}
	if selected := router.selectWithLocalBrain(&types.OpenAIRequest{}, candidates); selected != nil {
		t.Fatalf("expected unavailable local brain to return no override, got %#v", selected)
	}
}

func TestLocalBrainSelectionCooldownSkipsRepeatedFailedCalls(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer server.Close()

	router := &Server{brainURL: server.URL, brainModel: "local-brain"}
	candidates := []*catalog.ModelEntry{
		{Provider: "opencode", Model: "oc/free", ID: "oc/free", HealthStatus: "healthy"},
		{Provider: "codex", Model: "cx/paid", ID: "cx/paid", HealthStatus: "healthy"},
	}
	if selected := router.selectWithLocalBrain(&types.OpenAIRequest{}, candidates); selected != nil {
		t.Fatal("expected failed selector request to return no override")
	}
	if selected := router.selectWithLocalBrain(&types.OpenAIRequest{}, candidates); selected != nil {
		t.Fatal("expected cooldown to keep failed selector from overriding route")
	}
	if calls != 1 {
		t.Fatalf("expected one selector request during cooldown, got %d", calls)
	}
}

func TestLocalBrainSelectionFailureQuarantinesBrainModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer server.Close()
	router := New(&types.Config{Providers: []*types.Provider{
		{Name: "local-brain", Type: types.ProviderLocal, BaseURL: server.URL, Models: []string{"selector"}, Enabled: true},
		{Name: "codex", Type: types.ProviderCodex, CLIPath: "/bin/true", Models: []string{"cx/backup"}, Enabled: true},
	}})
	candidates := []*catalog.ModelEntry{
		{Provider: "local-brain", Model: "selector", ID: "local-brain/selector", HealthStatus: "healthy"},
		{Provider: "codex", Model: "cx/backup", ID: "cx/backup", HealthStatus: "healthy"},
	}
	if selected := router.selectWithLocalBrain(&types.OpenAIRequest{}, candidates); selected != nil {
		t.Fatal("expected failed selector request to return no override")
	}
	entry := router.catalog.GetModel("local-brain/selector")
	if entry == nil || entry.HealthStatus != health.HealthCooldown {
		t.Fatalf("expected Brain model to enter cooldown, got %+v", entry)
	}
}

func TestExtractSelectionModelAcceptsTruncatedJSON(t *testing.T) {
	if got := extractSelectionModel(`{"model":"cx/gpt-5.4-mini","reason":"bounded`); got != "cx/gpt-5.4-mini" {
		t.Fatalf("expected model from truncated selector JSON, got %q", got)
	}
	if got := extractSelectionModel(`not json`); got != "" {
		t.Fatalf("expected malformed selector output to fail closed, got %q", got)
	}
}

func TestAutomaticVirtualRouteUsesLocalBrainBeforeListScoring(t *testing.T) {
	brainCalls := 0
	brain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		brainCalls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"model\":\"free/model\",\"reason\":\"fast backup\"}"}}]}`))
	}))
	defer brain.Close()

	router := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "local-brain", Type: types.ProviderLocal, BaseURL: brain.URL, Models: []string{"selector"}, Enabled: true},
			{Name: "free", Type: types.ProviderCustom, CLIPath: "/bin/true", Models: []string{"free/model"}, Enabled: true},
		},
		ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"local-brain/selector", "free/model"}}},
	})
	req := &types.OpenAIRequest{Model: "ghrouter/auto", Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}}
	provider, model := router.RouteOpenAIRequest(req)
	if provider != "free" || model != "free/model" || req.SelectionStage != "local_brain" || brainCalls != 1 {
		t.Fatalf("expected Brain-first automatic route, got %s/%s stage=%q calls=%d", provider, model, req.SelectionStage, brainCalls)
	}
	explanation := router.ExplainRequest(&types.OpenAIRequest{Model: "ghrouter/auto", Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}})
	if explanation.SelectionSource != "local_brain" || explanation.SelectionReason != "fast backup" {
		t.Fatalf("expected explain to preserve Brain selection source, got %+v", explanation)
	}
}

func TestAutomaticVirtualRouteKeepsFreeFirstBeforeLocalBrain(t *testing.T) {
	brainCalls := 0
	brain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		brainCalls++
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"model\":\"paid/model\"}"}}]}`))
	}))
	defer brain.Close()

	router := New(&types.Config{
		Providers: []*types.Provider{
			{Name: "local-brain", Type: types.ProviderLocal, BaseURL: brain.URL, Models: []string{"selector"}, Enabled: false},
			{Name: "free", Type: types.ProviderCustom, CLIPath: "/bin/true", Models: []string{"free/model"}, Enabled: true, ModelInfo: map[string]types.ModelInfo{
				"free/model": {CostTier: "free", HealthStatus: "healthy"},
			}},
			{Name: "paid", Type: types.ProviderCustom, CLIPath: "/bin/true", Models: []string{"paid/model"}, Enabled: true, ModelInfo: map[string]types.ModelInfo{
				"paid/model": {CostTier: "premium", HealthStatus: "healthy"},
			}},
		},
		ModelLists: []types.ModelList{{Name: "ghrouter/auto", Kind: "automatic", Models: []string{"free/model", "paid/model"}}},
	})

	provider, model := router.RouteOpenAIRequest(&types.OpenAIRequest{Model: "ghrouter/auto", Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}})
	if provider != "free" || model != "free/model" || brainCalls != 0 {
		t.Fatalf("expected free-first deterministic route before Brain, got %s/%s brain_calls=%d", provider, model, brainCalls)
	}
}

func TestModelSummaryExposesEvidenceBackedClassifications(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{{
		Name: "opencode", Type: types.ProviderOpenCode, Models: []string{"oc/free"}, Enabled: true,
		ModelInfo: map[string]types.ModelInfo{"oc/free": {CostTier: "free"}},
	}}})
	models := srv.ModelSummaries()
	if len(models) != 1 {
		t.Fatalf("expected one model summary, got %d", len(models))
	}
	joined := strings.Join(models[0].Classifications, ",")
	for _, marker := range []string{"cost:free", "latency:unknown", "provenance:configured"} {
		if !strings.Contains(joined, marker) {
			t.Fatalf("expected classification %q in %q", marker, joined)
		}
	}
}

func TestModelSummaryDoesNotInferFreeCostWithoutEvidence(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{{
		Name: "codex", Type: types.ProviderCodex, Models: []string{"cx/unknown"}, Enabled: true,
	}}})
	models := srv.ModelSummaries()
	if len(models) != 1 || models[0].CostTier != string(catalog.CostUnknown) {
		t.Fatalf("expected unknown cost without provider evidence, got %+v", models)
	}
	if !strings.Contains(strings.Join(models[0].Classifications, ","), "cost:unknown") {
		t.Fatalf("expected unknown cost classification, got %v", models[0].Classifications)
	}
}

func TestExplainRequestShowsDecisionEvidence(t *testing.T) {
	srv := New(&types.Config{Providers: []*types.Provider{
		{Name: "free", Type: types.ProviderCustom, CLIPath: "/bin/true", Models: []string{"free-model"}, Enabled: true, ModelInfo: map[string]types.ModelInfo{"free-model": {CostTier: "free", ContextWindow: 128000}}},
		{Name: "basic", Type: types.ProviderCustom, CLIPath: "/bin/true", Models: []string{"basic-model"}, Enabled: true},
	}})
	explanation := srv.ExplainRequest(&types.OpenAIRequest{Messages: []types.OpenAIMessage{{Role: "user", Content: "fix this code quickly"}}})
	if explanation.SelectionSource != "deterministic_score" || explanation.Selected == nil {
		t.Fatalf("expected deterministic selected candidate, got %+v", explanation)
	}
	if explanation.Profile.Intent != IntentCode || explanation.Slot != string(catalog.SlotFastCode) {
		t.Fatalf("expected code/fast-code profile, got %+v", explanation)
	}
	if !explanation.Selected.Eligible || explanation.Selected.Score <= 0 {
		t.Fatalf("expected selected candidate evidence, got %+v", explanation.Selected)
	}
	if len(explanation.Candidates) != 2 {
		t.Fatalf("expected both catalog candidates in explanation, got %+v", explanation.Candidates)
	}
}

func TestPrioritizeModelCandidatesKeepsFreeFirstButRequiresQualityForComplexWork(t *testing.T) {
	freeBasic := &catalog.ModelEntry{ID: "oc/basic", CostTier: catalog.CostFree, HealthStatus: "healthy"}
	freeReasoning := &catalog.ModelEntry{ID: "oc/reasoning", CostTier: catalog.CostFree, Thinking: true, HealthStatus: "healthy"}
	paidReasoning := &catalog.ModelEntry{ID: "cx/reasoning", CostTier: catalog.CostPremium, Thinking: true, HealthStatus: "healthy"}

	normal := prioritizeModelCandidates([]*catalog.ModelEntry{paidReasoning, freeBasic}, RequestProfile{Intent: IntentChat}, catalog.SlotAuto)
	if len(normal) != 1 || normal[0] != freeBasic {
		t.Fatalf("expected free model first for normal work, got %#v", normal)
	}
	complex := prioritizeModelCandidates([]*catalog.ModelEntry{freeBasic, paidReasoning}, RequestProfile{Intent: IntentReasoning}, catalog.SlotAuto)
	if len(complex) != 1 || complex[0] != paidReasoning {
		t.Fatalf("expected qualified reasoning model for complex work, got %#v", complex)
	}
	complexFree := prioritizeModelCandidates([]*catalog.ModelEntry{freeBasic, freeReasoning, paidReasoning}, RequestProfile{Intent: IntentReasoning}, catalog.SlotAuto)
	if len(complexFree) != 1 || complexFree[0] != freeReasoning {
		t.Fatalf("expected qualified free reasoning model first, got %#v", complexFree)
	}
}

func TestVirtualFallbackKeepsPolicyPriorityAfterPrimaryFailure(t *testing.T) {
	router := &Server{
		cfg: &types.Config{Providers: []*types.Provider{
			{Name: "paid", Enabled: true},
			{Name: "free", Enabled: true},
		}},
		catalog: catalog.NewCatalog(nil, time.Minute),
	}
	router.catalog.RegisterProvider("paid", []*catalog.ModelEntry{{
		ID: "paid/model", Provider: "paid", Model: "model", CostTier: catalog.CostPremium,
		HealthStatus: health.HealthHealthy, LatencyP50: 10 * time.Millisecond,
	}})
	router.catalog.RegisterProvider("free", []*catalog.ModelEntry{{
		ID: "free/model", Provider: "free", Model: "model", CostTier: catalog.CostFree,
		HealthStatus: health.HealthHealthy, LatencyP50: 2 * time.Second,
	}})
	req := &types.OpenAIRequest{Model: "ghrouter/auto", Messages: []types.OpenAIMessage{{Role: "user", Content: "hello"}}}
	ranked := router.rankRouteCandidates([]routeCandidate{{provider: "paid", model: "model"}, {provider: "free", model: "model"}}, req)
	if len(ranked) != 2 || ranked[0].provider != "free" {
		t.Fatalf("expected free candidate to remain first in virtual fallback, got %+v", ranked)
	}
}

func TestPrioritizeModelCandidatesDoesNotReintroduceModelOutsideSlot(t *testing.T) {
	vision := &catalog.ModelEntry{ID: "oc/vision", Vision: true, HealthStatus: "healthy"}
	reasoning := &catalog.ModelEntry{ID: "cx/reasoning", Thinking: true, HealthStatus: "healthy"}
	profile := RequestProfile{Intent: IntentVision, ReasoningEffort: "high"}

	selected := prioritizeModelCandidates([]*catalog.ModelEntry{vision, reasoning}, profile, catalog.SlotVision)
	if len(selected) != 1 || selected[0] != vision {
		t.Fatalf("expected slot-compatible vision model to remain selected, got %#v", selected)
	}
}

func TestBestScoredCandidateDoesNotReduceComplexWorkToFastBackup(t *testing.T) {
	strong := &catalog.ModelEntry{Provider: "codex", Model: "reasoning", ID: "cx/reasoning", Thinking: true, HealthStatus: "healthy"}
	fast := &catalog.ModelEntry{Provider: "opencode", Model: "fast", ID: "oc/fast", LatencyP50: 400 * time.Millisecond, HealthStatus: "healthy"}
	router := &Server{cfg: &types.Config{}, catalog: catalog.NewCatalog(nil, time.Minute)}
	selected := bestScoredCandidate(router, []*catalog.ModelEntry{strong, fast}, &types.OpenAIRequest{
		ReasoningEffort: "high",
		Messages:        []types.OpenAIMessage{{Role: "user", Content: "analyze the architecture and explain the tradeoffs"}},
	})
	if selected != strong {
		t.Fatalf("expected complex request to retain strong candidate, got %#v", selected)
	}
}

func TestContextCapacityScoreOnlyAffectsComplexWork(t *testing.T) {
	if got := contextCapacityScore(RequestProfile{Complexity: ComplexityLow}, 1_000_000); got != 0 {
		t.Fatalf("simple work must not receive context bonus, got %v", got)
	}
	complex := RequestProfile{Complexity: ComplexityHigh}
	if got := contextCapacityScore(complex, 1_000_000); got <= contextCapacityScore(complex, 128_000) {
		t.Fatalf("expected larger context bonus for complex work, got 1m=%v 128k=%v", got, contextCapacityScore(complex, 128_000))
	}
	if got := contextCapacityScore(complex, 128_000); got <= contextCapacityScore(complex, 32_000) {
		t.Fatalf("expected context tiers to be ordered, got 128k=%v 32k=%v", got, contextCapacityScore(complex, 32_000))
	}
}
