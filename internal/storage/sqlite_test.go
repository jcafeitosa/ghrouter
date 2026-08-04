package storage

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestStorePersistsRedactedRequestAndUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghrouter.db")
	store, err := Open(Config{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if !store.RecordRequest(RequestEvent{
		RequestID:    "req_test",
		Endpoint:     "/v1/chat/completions",
		ConnectionID: "primary",
		Provider:     "opencode",
		Model:        "oc/model",
		Status:       "ok",
		LatencyMS:    42,
		PromptTokens: 3, CompletionTokens: 5, CostMicros: 17,
		DecisionJSON: `{"intent":"code","graph":{"stages":["plan","implement","verify"]}}`,
		Attempts:     []AttemptEvent{{ProviderID: "opencode", ModelID: "oc/model", ConnectionID: "primary", Status: "ok", LatencyMS: 42}},
		At:           time.Now(),
	}) {
		t.Fatal("expected request event to be queued")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var requests, usage, tokens, costs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM request_history WHERE request_id = 'req_test'`).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT requests FROM usage_totals WHERE provider_id = 'opencode' AND model_id = 'oc/model'`).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT estimated_tokens FROM usage_totals WHERE provider_id = 'opencode' AND model_id = 'oc/model'`).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT estimated_cost_micros FROM usage_totals WHERE provider_id = 'opencode' AND model_id = 'oc/model'`).Scan(&costs); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || usage != 1 || tokens != 8 || costs != 17 {
		t.Fatalf("expected one request, usage row, eight tokens and cost, got requests=%d usage=%d tokens=%d costs=%d", requests, usage, tokens, costs)
	}
	var attempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM attempt_history WHERE request_id = 'req_test'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("expected one persisted attempt, got %d", attempts)
	}
	var requestConnection, attemptConnection string
	if err := db.QueryRow(`SELECT connection_id FROM request_history WHERE request_id = 'req_test'`).Scan(&requestConnection); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT connection_id FROM attempt_history WHERE request_id = 'req_test'`).Scan(&attemptConnection); err != nil {
		t.Fatal(err)
	}
	if requestConnection != "primary" || attemptConnection != "primary" {
		t.Fatalf("expected connection correlation, got request=%q attempt=%q", requestConnection, attemptConnection)
	}
	var decision string
	if err := db.QueryRow(`SELECT decision_json FROM request_history WHERE request_id = 'req_test'`).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decision, `"intent":"code"`) {
		t.Fatalf("expected redacted brain decision, got %s", decision)
	}
}

func TestPruneBeforeRemovesOldRawHistoryAndPreservesRecentAndAggregates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghrouter.db")
	store, err := Open(Config{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	recent := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO request_history(request_id, endpoint, status, started_at) VALUES (?, ?, ?, ?)`, []any{"old", "/health", "ok", old}},
		{`INSERT INTO request_history(request_id, endpoint, status, started_at) VALUES (?, ?, ?, ?)`, []any{"recent", "/health", "ok", recent}},
		{`INSERT INTO attempt_history(request_id, attempt_number, status, started_at) VALUES (?, ?, ?, ?)`, []any{"old", 1, "ok", old}},
		{`INSERT INTO health_samples(provider_id, status, observed_at) VALUES (?, ?, ?)`, []any{"old", "healthy", old}},
		{`INSERT INTO health_samples(provider_id, status, observed_at) VALUES (?, ?, ?)`, []any{"recent", "healthy", recent}},
		{`INSERT INTO audit_events(action, created_at) VALUES (?, ?)`, []any{"old", old}},
		{`INSERT INTO config_snapshots(checksum, path, created_at) VALUES (?, ?, ?)`, []any{"old", "test", old}},
		{`INSERT INTO usage_totals(provider_id, model_id, requests, updated_at) VALUES (?, ?, ?, ?)`, []any{"provider", "model", 7, old}},
	} {
		if _, err := store.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	report, err := store.PruneBefore(time.Now().UTC().Add(-24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if report.Requests != 1 || report.Attempts != 1 || report.Health != 1 || report.Audit != 1 || report.Snapshots != 1 {
		t.Fatalf("unexpected retention report: %+v", report)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM request_history`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected recent request to remain, got %d", count)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM usage_totals`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected aggregate usage to remain, got %d", count)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAppliesConfiguredRetentionOnStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghrouter.db")
	store, err := Open(Config{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-72 * time.Hour).Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO health_samples(provider_id, status, observed_at) VALUES (?, ?, ?)`, "old", "healthy", old); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(Config{Enabled: true, Path: path, RetentionDays: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM health_samples`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected startup retention to remove old sample, got %d", count)
	}
}

func TestStoreMigratesLegacyModelCatalogCooldownColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`CREATE TABLE provider_catalog (provider_id TEXT PRIMARY KEY, cli_type TEXT NOT NULL, executable TEXT NOT NULL, auth_state TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_catalog (model_id TEXT PRIMARY KEY, provider_id TEXT NOT NULL, capabilities_json TEXT NOT NULL DEFAULT '[]', slots_json TEXT NOT NULL DEFAULT '[]', health_state TEXT NOT NULL, max_tokens INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL)`,
	}
	for _, statement := range legacy {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(Config{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cooldown := time.Now().Add(time.Minute)
	if err := store.ReplaceCatalog([]ProviderRecord{{ProviderID: "legacy", CLIType: "custom"}}, []ModelRecord{{ModelID: "legacy/model", ProviderID: "legacy", HealthState: "cooldown", CooldownUntil: cooldown}}); err != nil {
		t.Fatal(err)
	}
	records, err := store.LoadModelCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || !records[0].CooldownUntil.Equal(cooldown) {
		t.Fatalf("expected migrated cooldown state, got %+v", records)
	}
	var version int
	if err := store.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != latestSchemaVersion {
		t.Fatalf("expected latest schema version %d, got %d", latestSchemaVersion, version)
	}
}

func TestStoreMigratesSchemaV3LatencyColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-v3.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`CREATE TABLE provider_catalog (provider_id TEXT PRIMARY KEY, cli_type TEXT NOT NULL, executable TEXT NOT NULL, auth_state TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_catalog (model_id TEXT PRIMARY KEY, provider_id TEXT NOT NULL, capabilities_json TEXT NOT NULL DEFAULT '[]', slots_json TEXT NOT NULL DEFAULT '[]', catalog_source TEXT NOT NULL DEFAULT '', discovered_at TEXT NOT NULL DEFAULT '', verified_at TEXT NOT NULL DEFAULT '', verification_error TEXT NOT NULL DEFAULT '', effort_json TEXT NOT NULL DEFAULT '[]', health_state TEXT NOT NULL, cost_tier TEXT NOT NULL DEFAULT 'unknown', token_cost INTEGER NOT NULL DEFAULT 0, context_window INTEGER NOT NULL DEFAULT 0, max_output INTEGER NOT NULL DEFAULT 0, max_tokens INTEGER NOT NULL DEFAULT 0, thinking INTEGER NOT NULL DEFAULT 0, vision INTEGER NOT NULL DEFAULT 0, tool_use INTEGER NOT NULL DEFAULT 0, cooldown_until TEXT NOT NULL DEFAULT '', failure_count INTEGER NOT NULL DEFAULT 0, error_rate REAL NOT NULL DEFAULT 0, last_health_check TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (1, 'legacy'), (2, 'legacy'), (3, 'legacy')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(Config{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ReplaceCatalog([]ProviderRecord{{ProviderID: "legacy"}}, []ModelRecord{{ModelID: "legacy/model", ProviderID: "legacy", HealthState: "healthy", LatencyP50: 150 * time.Millisecond, LatencyP95: 450 * time.Millisecond}}); err != nil {
		t.Fatal(err)
	}
	records, err := store.LoadModelCatalog()
	if err != nil || len(records) != 1 {
		t.Fatalf("expected migrated latency record, records=%+v err=%v", records, err)
	}
	if records[0].LatencyP50 != 150*time.Millisecond || records[0].LatencyP95 != 450*time.Millisecond {
		t.Fatalf("schema v3 latency columns were not migrated: %+v", records[0])
	}
}

func TestStoreMigratesSchemaV4CatalogEvidenceColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-v4.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`CREATE TABLE provider_catalog (provider_id TEXT PRIMARY KEY, cli_type TEXT NOT NULL, executable TEXT NOT NULL, auth_state TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_catalog (model_id TEXT PRIMARY KEY, provider_id TEXT NOT NULL, capabilities_json TEXT NOT NULL DEFAULT '[]', slots_json TEXT NOT NULL DEFAULT '[]', catalog_source TEXT NOT NULL DEFAULT '', effort_json TEXT NOT NULL DEFAULT '[]', health_state TEXT NOT NULL, cost_tier TEXT NOT NULL DEFAULT 'unknown', token_cost INTEGER NOT NULL DEFAULT 0, context_window INTEGER NOT NULL DEFAULT 0, max_output INTEGER NOT NULL DEFAULT 0, max_tokens INTEGER NOT NULL DEFAULT 0, thinking INTEGER NOT NULL DEFAULT 0, vision INTEGER NOT NULL DEFAULT 0, tool_use INTEGER NOT NULL DEFAULT 0, latency_p50_ms INTEGER NOT NULL DEFAULT 0, latency_p95_ms INTEGER NOT NULL DEFAULT 0, cooldown_until TEXT NOT NULL DEFAULT '', failure_count INTEGER NOT NULL DEFAULT 0, error_rate REAL NOT NULL DEFAULT 0, last_health_check TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL)`,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (1, 'legacy'), (2, 'legacy'), (3, 'legacy'), (4, 'legacy')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(Config{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	discovered := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	verified := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.ReplaceCatalog([]ProviderRecord{{ProviderID: "legacy"}}, []ModelRecord{{
		ModelID: "legacy/model", ProviderID: "legacy", HealthState: "healthy",
		DiscoveredAt: discovered, VerifiedAt: verified, VerificationErr: "",
	}}); err != nil {
		t.Fatal(err)
	}
	records, err := store.LoadModelCatalog()
	if err != nil || len(records) != 1 {
		t.Fatalf("expected migrated catalog evidence, records=%+v err=%v", records, err)
	}
	if !records[0].DiscoveredAt.Equal(discovered) || !records[0].VerifiedAt.Equal(verified) {
		t.Fatalf("catalog evidence was not persisted after migration: %+v", records[0])
	}
	var version int
	if err := store.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != latestSchemaVersion {
		t.Fatalf("expected latest schema version %d, got %d", latestSchemaVersion, version)
	}
}

func TestStoreRejectsUnsupportedFutureSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL); INSERT INTO schema_migrations(version, applied_at) VALUES (999, 'future');`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(Config{Enabled: true, Path: path})
	if store != nil {
		_ = store.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "newer than supported version") {
		t.Fatalf("expected future schema rejection, got store=%v err=%v", store, err)
	}
}

func TestDisabledStoreDoesNotCreateDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disabled.db")
	store, err := Open(Config{Enabled: false, Path: path})
	if err != nil || store != nil {
		t.Fatalf("expected disabled store to be nil without error, got store=%v err=%v", store, err)
	}
}

func TestStoreCheckConfirmsSQLiteRuntimeConfiguration(t *testing.T) {
	store, err := Open(Config{Enabled: true, Path: filepath.Join(t.TempDir(), "check.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Check(); err != nil {
		t.Fatalf("expected SQLite runtime configuration to be healthy: %v", err)
	}
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("expected one SQLite connection for consistent PRAGMAs, got %d", got)
	}
}

func TestStoreQueueOverflowIsObservable(t *testing.T) {
	store, err := Open(Config{Enabled: true, Path: filepath.Join(t.TempDir(), "ghrouter.db"), QueueSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	store.lifecycleMu.Lock()
	store.events <- RequestEvent{RequestID: "queued", Endpoint: "/health", Status: "ok", At: time.Now()}
	store.lifecycleMu.Unlock()

	if err := store.EnqueueRequest(RequestEvent{RequestID: "dropped", Endpoint: "/health", Status: "ok"}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected queue-full error, got %v", err)
	}
	if stats := store.Stats(); stats.Dropped != 1 {
		t.Fatalf("expected one dropped event, got %+v", stats)
	}
}

func TestStoreCloseRejectsEventsAndIsIdempotent(t *testing.T) {
	store, err := Open(Config{Enabled: true, Path: filepath.Join(t.TempDir(), "ghrouter.db")})
	if err != nil {
		t.Fatal(err)
	}

	firstErr := store.Close()
	secondErr := store.Close()
	if firstErr != nil || secondErr != nil {
		t.Fatalf("expected idempotent close, got first=%v second=%v", firstErr, secondErr)
	}
	if err := store.EnqueueRequest(RequestEvent{RequestID: "after-close"}); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected closed error, got %v", err)
	}
	if !store.Stats().Closed {
		t.Fatal("expected closed store stats")
	}
}

func TestStoreReportsWriterErrors(t *testing.T) {
	store, err := Open(Config{Enabled: true, Path: filepath.Join(t.TempDir(), "ghrouter.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	writeErrors := make(chan error, 1)
	store.SetWriteErrorHook(func(err error) { writeErrors <- err })
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueRequest(RequestEvent{RequestID: "write-error", Endpoint: "/health", Status: "ok"}); err != nil {
		t.Fatalf("expected event to enter queue, got %v", err)
	}

	select {
	case err := <-writeErrors:
		if err == nil || !strings.Contains(err.Error(), "database is closed") {
			t.Fatalf("expected database-closed error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for writer error hook")
	}
	stats := store.Stats()
	if stats.WriteErrors != 1 || stats.LastError == "" {
		t.Fatalf("expected observable writer error, got %+v", stats)
	}
}

func TestStoreRestrictsDatabasePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghrouter.db")
	store, err := Open(Config{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("expected database mode 0600, got %o", mode)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err = Open(Config{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("expected existing database repaired to 0600, got %o", mode)
	}
}

func TestStorePersistsCatalogHealthAndConfigSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghrouter.db")
	store, err := Open(Config{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	cooldown := time.Now().Add(time.Hour).UTC().Truncate(time.Nanosecond)
	if err := store.ReplaceCatalog([]ProviderRecord{{
		ProviderID: "codex", CLIType: "codex", Executable: "/bin/codex", AuthState: "ready",
	}}, []ModelRecord{{
		ModelID: "codex/model", ProviderID: "codex", Capabilities: []string{"code"}, Slots: []string{"auto"}, HealthState: "cooldown", MaxTokens: 4096, CooldownUntil: cooldown, FailureCount: 3, ErrorRate: 0.75,
	}}); err != nil {
		t.Fatal(err)
	}
	records, err := store.LoadModelCatalog()
	if err != nil || len(records) != 1 {
		t.Fatalf("expected one restorable model record, got records=%+v err=%v", records, err)
	}
	if records[0].HealthState != "cooldown" || records[0].FailureCount != 3 || records[0].ErrorRate != 0.75 || !records[0].CooldownUntil.Equal(cooldown) {
		t.Fatalf("model cooldown state was not durable: %+v", records[0])
	}
	if err := store.RecordHealthSample(HealthSample{ProviderID: "codex", Status: "healthy", LatencyMS: 12, ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordConfigSnapshot(ConfigSnapshot{Checksum: "sha256:test", Path: "config.yaml", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var providers, models, samples, snapshots int
	for query, target := range map[string]*int{
		"SELECT COUNT(*) FROM provider_catalog": &providers,
		"SELECT COUNT(*) FROM model_catalog":    &models,
		"SELECT COUNT(*) FROM health_samples":   &samples,
		"SELECT COUNT(*) FROM config_snapshots": &snapshots,
	} {
		if err := db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if providers != 1 || models != 1 || samples != 1 || snapshots != 1 {
		t.Fatalf("expected durable catalog and health state, got providers=%d models=%d samples=%d snapshots=%d", providers, models, samples, snapshots)
	}
}

func TestStorePersistsNativeModelMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.db")
	store, err := Open(Config{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	discoveredAt := time.Now().UTC().Add(-time.Hour)
	verifiedAt := time.Now().UTC()
	model := ModelRecord{ModelID: "opencode/model", ProviderID: "opencode", CatalogSource: "native", DiscoveredAt: discoveredAt, VerifiedAt: verifiedAt, VerificationErr: "", Effort: []string{"high", "max"}, Capabilities: []string{"long-context", "tool-use"}, Slots: []string{"context-1m"}, HealthState: "healthy", CostTier: "premium", TokenCost: 1200, ContextWindow: 1000000, MaxOutput: 128000, MaxTokens: 128000, Thinking: true, Vision: true, ToolUse: true, LatencyP50: 200 * time.Millisecond, LatencyP95: 500 * time.Millisecond}
	if err := store.ReplaceCatalog([]ProviderRecord{{ProviderID: "opencode"}}, []ModelRecord{model}); err != nil {
		t.Fatal(err)
	}
	records, err := store.LoadModelCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ContextWindow != 1000000 || records[0].TokenCost != 1200 || records[0].CostTier != "premium" || !records[0].Thinking || !records[0].ToolUse || len(records[0].Effort) != 2 || !records[0].DiscoveredAt.Equal(discoveredAt) || !records[0].VerifiedAt.Equal(verifiedAt) || records[0].LatencyP50 != 200*time.Millisecond || records[0].LatencyP95 != 500*time.Millisecond {
		t.Fatalf("native metadata was not preserved: %+v", records)
	}
}

func TestStoreRedactsSensitiveAuditDetails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghrouter.db")
	store, err := Open(Config{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAudit("test", "local", map[string]any{"token": "secret-value", "port": 9090}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var details string
	if err := db.QueryRow(`SELECT details_redacted FROM audit_events WHERE action = 'test'`).Scan(&details); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(details, "secret-value") || !strings.Contains(details, "9090") {
		t.Fatalf("expected sensitive audit value redacted while safe value remains, got %s", details)
	}
}

func TestStoreListsAuditEvents(t *testing.T) {
	store, err := Open(Config{Enabled: true, Path: filepath.Join(t.TempDir(), "audit.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.RecordAudit("test_action", "admin", map[string]any{"name": "safe"}); err != nil {
		t.Fatal(err)
	}
	events, err := store.ListAudit(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "test_action" || events[0].Actor != "admin" {
		t.Fatalf("unexpected audit events: %+v", events)
	}
}

func TestStorePersistsControlPlaneResources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghrouter.db")
	store, err := Open(Config{Enabled: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceControlPlane(
		[]ConnectionRecord{{Name: "copilot", Provider: "codex", Model: "cx/model", Enabled: true}},
		[]PoolRecord{{Name: "fast", Members: []string{"codex/cx/model"}, Strategy: "round-robin", Enabled: true}},
		[]ComboRecord{{Name: "review", Members: []string{"codex/cx/model"}, Strategy: "score", Judge: "codex/cx/model", Enabled: true}},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"connections", "pools", "combos"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("expected one row in %s, got %d", table, count)
		}
	}
}
