package server

import (
	"testing"
	"time"

	"ghrouter/internal/catalog"
	"ghrouter/internal/types"
)

func TestFastBackupCandidatesPreferObservedFastModels(t *testing.T) {
	slow := &catalog.ModelEntry{ID: "slow/model", LatencyP50: 4 * time.Second}
	fast := &catalog.ModelEntry{ID: "fast/model", LatencyP50: 500 * time.Millisecond}
	got := fastBackupCandidates([]*catalog.ModelEntry{slow, fast})
	if len(got) != 1 || got[0] != fast {
		t.Fatalf("expected only observed fast backup, got %+v", got)
	}
}

func TestProviderHasHealthyCatalogModelRequiresHealthyEntry(t *testing.T) {
	if providerHasHealthyCatalogModel([]*catalog.ModelEntry{{HealthStatus: "cooldown", CooldownUntil: time.Now().Add(time.Minute)}}) {
		t.Fatal("expected cooldown-only provider to be unavailable")
	}
	if !providerHasHealthyCatalogModel([]*catalog.ModelEntry{{HealthStatus: "healthy"}}) {
		t.Fatal("expected healthy catalog model to keep provider eligible")
	}
	if !providerHasHealthyCatalogModel([]*catalog.ModelEntry{{HealthStatus: "unknown"}}) {
		t.Fatal("expected unverified catalog model to remain eligible")
	}
}

func TestReadinessRequiresVerifiedHealthyModel(t *testing.T) {
	provider := &types.Provider{Name: "codex", Type: types.ProviderCodex, Models: []string{"cx/model"}, Enabled: true}
	srv := NewWithConfigPath(&types.Config{Providers: []*types.Provider{provider}}, "/tmp/readiness-test-config.yaml")
	provider.ModelInfo = map[string]types.ModelInfo{"cx/model": {VerifiedAt: time.Now().UTC()}}
	if srv.hasVerifiedHealthyModel() {
		t.Fatal("expected unknown catalog model to be insufficient for readiness")
	}
	srv.catalog.RecordSuccess("cx/model", time.Now())
	if !srv.hasVerifiedHealthyModel() {
		t.Fatal("expected verified healthy catalog model to satisfy readiness")
	}
}
