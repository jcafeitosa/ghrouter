package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"ghrouter/internal/types"
)

func TestBrainConcurrentRoutingAndExplanationStress(t *testing.T) {
	var selections atomic.Int64
	brain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selections.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]string{"content": `{"model":"free/free-model","reason":"stress"}`},
			}},
		})
	}))
	defer brain.Close()

	srv := New(&types.Config{Providers: []*types.Provider{
		{Name: "local-brain", Type: types.ProviderLocal, BaseURL: brain.URL, Models: []string{"selector"}, Enabled: true},
		{Name: "free", Type: types.ProviderCustom, CLIPath: "/bin/true", Models: []string{"free-model"}, Enabled: true},
		{Name: "backup", Type: types.ProviderCustom, CLIPath: "/bin/true", Models: []string{"backup-model"}, Enabled: true},
	}})

	const workers = 32
	const iterations = 24
	errors := make(chan string, workers*iterations)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				req := &types.OpenAIRequest{Messages: []types.OpenAIMessage{{Role: "user", Content: "route this normal request"}}}
				provider, model := srv.RouteOpenAIRequest(req)
				if provider != "free" || model != "free-model" {
					errors <- provider + "/" + model
				}
				if iteration%4 == 0 {
					explanation := srv.ExplainRequest(req)
					if explanation.Selected == nil || explanation.Selected.ID != "free/free-model" {
						errors <- "explain:" + explanation.SelectionSource
					}
				}
			}
		}()
	}
	group.Wait()
	close(errors)
	for route := range errors {
		t.Errorf("unexpected stress route: %s", route)
	}
	if selections.Load() == 0 {
		t.Fatal("expected local brain to receive concurrent selections")
	}
}
