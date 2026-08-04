package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ghrouter/internal/observability"
	_ "modernc.org/sqlite"
)

type Config struct {
	Enabled       bool
	Path          string
	QueueSize     int
	RetentionDays int
}

type RetentionReport struct {
	Requests  int64
	Attempts  int64
	Health    int64
	Audit     int64
	Snapshots int64
}

var (
	ErrStoreClosed = errors.New("storage: store is closed")
	ErrQueueFull   = errors.New("storage: event queue is full")
)

type RequestEvent struct {
	RequestID        string
	Client           string
	Endpoint         string
	ConnectionID     string
	Provider         string
	Model            string
	Status           string
	Fallback         bool
	LatencyMS        int64
	At               time.Time
	PromptTokens     int
	CompletionTokens int
	CostMicros       int64
	DecisionJSON     string
	Attempts         []AttemptEvent
}

type AttemptEvent struct {
	ProviderID   string
	ModelID      string
	ConnectionID string
	Status       string
	Error        string
	LatencyMS    int64
	StartedAt    time.Time
}

type ProviderRecord struct {
	ProviderID string
	CLIType    string
	Executable string
	AuthState  string
}

type ModelRecord struct {
	ModelID         string
	ProviderID      string
	Capabilities    []string
	Slots           []string
	CatalogSource   string
	DiscoveredAt    time.Time
	VerifiedAt      time.Time
	VerificationErr string
	Effort          []string
	HealthState     string
	CostTier        string
	TokenCost       int
	ContextWindow   int
	MaxOutput       int
	MaxTokens       int
	Thinking        bool
	Vision          bool
	ToolUse         bool
	LatencyP50      time.Duration
	LatencyP95      time.Duration
	CooldownUntil   time.Time
	FailureCount    int
	ErrorRate       float64
	LastHealthCheck time.Time
}

type HealthSample struct {
	ProviderID string
	Status     string
	LatencyMS  int64
	Error      string
	ObservedAt time.Time
}

type ConfigSnapshot struct {
	Checksum  string
	Path      string
	CreatedAt time.Time
}

type AuditEvent struct {
	Action    string
	Actor     string
	Details   string
	CreatedAt time.Time
}

type ConnectionRecord struct {
	Name     string
	Provider string
	Model    string
	Enabled  bool
	Metadata map[string]string
}

type PoolRecord struct {
	Name     string
	Members  []string
	Strategy string
	Enabled  bool
}

type ComboRecord struct {
	Name     string
	Members  []string
	Strategy string
	Judge    string
	Enabled  bool
}

type Store struct {
	db          *sql.DB
	path        string
	events      chan RequestEvent
	done        chan struct{}
	wg          sync.WaitGroup
	closeOnce   sync.Once
	closeErr    error
	lifecycleMu sync.Mutex
	closed      bool
	statsMu     sync.RWMutex
	lastErr     error
	errorHook   func(error)
	queued      atomic.Int64
	dropped     atomic.Int64
	written     atomic.Int64
	writeErrs   atomic.Int64
}

type Stats struct {
	Queued      int64  `json:"queued"`
	Dropped     int64  `json:"dropped"`
	Written     int64  `json:"written"`
	WriteErrors int64  `json:"write_errors"`
	QueueDepth  int    `json:"queue_depth"`
	Closed      bool   `json:"closed"`
	LastError   string `json:"last_error,omitempty"`
}

const latestSchemaVersion = 5

func Open(cfg Config) (*Store, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		path = defaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create storage directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize sqlite: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("restrict sqlite permissions: %w", err)
	}
	if err := configure(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = 256
	}
	s := &Store{db: db, path: path, events: make(chan RequestEvent, queueSize), done: make(chan struct{})}
	if cfg.RetentionDays > 0 {
		if _, err := s.PruneBefore(time.Now().UTC().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)); err != nil {
			db.Close()
			return nil, fmt.Errorf("prune sqlite retention: %w", err)
		}
	}
	s.wg.Add(1)
	go s.writeLoop()
	return s, nil
}

func (s *Store) PruneBefore(cutoff time.Time) (RetentionReport, error) {
	var report RetentionReport
	if s == nil || s.db == nil {
		return report, ErrStoreClosed
	}
	cutoffText := cutoff.UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return report, fmt.Errorf("begin retention prune: %w", err)
	}
	rollback := func(cause error) (RetentionReport, error) {
		_ = tx.Rollback()
		return RetentionReport{}, cause
	}
	queries := []struct {
		table string
		query string
		args  []any
		count *int64
	}{
		{table: "attempt_history", query: `DELETE FROM attempt_history WHERE request_id IN (SELECT request_id FROM request_history WHERE started_at < ?)`, args: []any{cutoffText}, count: &report.Attempts},
		{table: "request_history", query: `DELETE FROM request_history WHERE started_at < ?`, args: []any{cutoffText}, count: &report.Requests},
		{table: "health_samples", query: `DELETE FROM health_samples WHERE observed_at < ?`, args: []any{cutoffText}, count: &report.Health},
		{table: "audit_events", query: `DELETE FROM audit_events WHERE created_at < ?`, args: []any{cutoffText}, count: &report.Audit},
		{table: "config_snapshots", query: `DELETE FROM config_snapshots WHERE created_at < ?`, args: []any{cutoffText}, count: &report.Snapshots},
	}
	for _, item := range queries {
		result, execErr := tx.Exec(item.query, item.args...)
		if execErr != nil {
			return rollback(fmt.Errorf("prune %s: %w", item.table, execErr))
		}
		count, countErr := result.RowsAffected()
		if countErr != nil {
			return rollback(fmt.Errorf("count pruned %s: %w", item.table, countErr))
		}
		*item.count = count
	}
	if err := tx.Commit(); err != nil {
		return RetentionReport{}, fmt.Errorf("commit retention prune: %w", err)
	}
	return report, nil
}

func defaultPath() string {
	if root, err := os.UserCacheDir(); err == nil && root != "" {
		return filepath.Join(root, "ghrouter", "ghrouter.db")
	}
	return filepath.Join(os.TempDir(), "ghrouter", "ghrouter.db")
}

func configure(db *sql.DB) error {
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=2500`,
		`PRAGMA foreign_keys=ON`,
	} {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("configure sqlite: %w", err)
		}
	}
	return nil
}

func (s *Store) Check() error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return ErrStoreClosed
	}
	if err := s.db.Ping(); err != nil {
		return fmt.Errorf("sqlite ping: %w", err)
	}
	var foreignKeys, busyTimeout int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read sqlite foreign_keys: %w", err)
	}
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		return fmt.Errorf("read sqlite busy_timeout: %w", err)
	}
	var journalMode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		return fmt.Errorf("read sqlite journal_mode: %w", err)
	}
	if foreignKeys != 1 || busyTimeout < 2500 || !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("sqlite pragmas not ready: foreign_keys=%d busy_timeout=%d journal_mode=%s", foreignKeys, busyTimeout, journalMode)
	}
	return nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS provider_catalog (
  provider_id TEXT PRIMARY KEY,
  cli_type TEXT NOT NULL,
  executable TEXT NOT NULL,
  auth_state TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS model_catalog (
  model_id TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL,
  capabilities_json TEXT NOT NULL DEFAULT '[]',
  slots_json TEXT NOT NULL DEFAULT '[]',
  health_state TEXT NOT NULL,
  max_tokens INTEGER NOT NULL DEFAULT 0,
  cooldown_until TEXT NOT NULL DEFAULT '',
  failure_count INTEGER NOT NULL DEFAULT 0,
  error_rate REAL NOT NULL DEFAULT 0,
  last_health_check TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  FOREIGN KEY(provider_id) REFERENCES provider_catalog(provider_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS health_samples (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  provider_id TEXT NOT NULL,
  status TEXT NOT NULL,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  error_redacted TEXT NOT NULL DEFAULT '',
  observed_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS request_history (
  request_id TEXT PRIMARY KEY,
  client TEXT NOT NULL DEFAULT '',
  endpoint TEXT NOT NULL,
  connection_id TEXT NOT NULL DEFAULT '',
  provider_id TEXT NOT NULL DEFAULT '',
  model_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  fallback INTEGER NOT NULL DEFAULT 0,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  prompt_tokens INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  cost_micros INTEGER NOT NULL DEFAULT 0,
  decision_json TEXT NOT NULL DEFAULT '{}',
  started_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS attempt_history (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL,
  attempt_number INTEGER NOT NULL,
  provider_id TEXT NOT NULL DEFAULT '',
  model_id TEXT NOT NULL DEFAULT '',
  connection_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  error_redacted TEXT NOT NULL DEFAULT '',
  cost_micros INTEGER NOT NULL DEFAULT 0,
  started_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS usage_totals (
  provider_id TEXT NOT NULL,
  model_id TEXT NOT NULL,
  requests INTEGER NOT NULL DEFAULT 0,
  failures INTEGER NOT NULL DEFAULT 0,
  estimated_tokens INTEGER NOT NULL DEFAULT 0,
  estimated_cost_micros INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(provider_id, model_id)
);
CREATE TABLE IF NOT EXISTS audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  action TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT '',
  details_redacted TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS config_snapshots (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  checksum TEXT NOT NULL,
  path TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS connections (
  name TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL,
  model_id TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  metadata_json TEXT NOT NULL DEFAULT '{}',
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS pools (
  name TEXT PRIMARY KEY,
  members_json TEXT NOT NULL DEFAULT '[]',
  strategy TEXT NOT NULL DEFAULT 'round-robin',
  enabled INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS combos (
  name TEXT PRIMARY KEY,
  members_json TEXT NOT NULL DEFAULT '[]',
  strategy TEXT NOT NULL DEFAULT 'score',
  judge TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_request_history_started ON request_history(started_at);
CREATE INDEX IF NOT EXISTS idx_attempt_history_request ON attempt_history(request_id, attempt_number);
CREATE INDEX IF NOT EXISTS idx_health_samples_observed ON health_samples(observed_at);
CREATE INDEX IF NOT EXISTS idx_config_snapshots_created ON config_snapshots(created_at);
`)
	if err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}
	current, err := schemaVersion(db)
	if err != nil {
		return err
	}
	if current > latestSchemaVersion {
		return fmt.Errorf("sqlite schema version %d is newer than supported version %d", current, latestSchemaVersion)
	}
	for version := current + 1; version <= latestSchemaVersion; version++ {
		if err := applyMigration(db, version); err != nil {
			return fmt.Errorf("apply sqlite migration %d: %w", version, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("record sqlite migration %d: %w", version, err)
		}
	}
	return nil
}

func schemaVersion(db *sql.DB) (int, error) {
	var version sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read sqlite schema version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

func applyMigration(db *sql.DB, version int) error {
	switch version {
	case 1:
		return nil
	case 2:
		return applyCatalogAndTelemetryColumns(db)
	case 3:
		return ensureColumn(db, "model_catalog", "cost_tier", "TEXT NOT NULL DEFAULT 'unknown'")
	case 4:
		if err := ensureColumn(db, "model_catalog", "latency_p50_ms", "INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
		return ensureColumn(db, "model_catalog", "latency_p95_ms", "INTEGER NOT NULL DEFAULT 0")
	case 5:
		for _, column := range []struct {
			name       string
			definition string
		}{
			{name: "discovered_at", definition: "TEXT NOT NULL DEFAULT ''"},
			{name: "verified_at", definition: "TEXT NOT NULL DEFAULT ''"},
			{name: "verification_error", definition: "TEXT NOT NULL DEFAULT ''"},
		} {
			if err := ensureColumn(db, "model_catalog", column.name, column.definition); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown sqlite migration version %d", version)
	}
}

func applyCatalogAndTelemetryColumns(db *sql.DB) error {
	requestColumns := []struct {
		name       string
		definition string
	}{
		{name: "prompt_tokens", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "completion_tokens", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "total_tokens", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "cost_micros", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "connection_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "decision_json", definition: "TEXT NOT NULL DEFAULT '{}'"},
	}
	for _, column := range requestColumns {
		if err := ensureColumn(db, "request_history", column.name, column.definition); err != nil {
			return err
		}
	}
	for table, columns := range map[string][]struct {
		name       string
		definition string
	}{
		"attempt_history": {{name: "cost_micros", definition: "INTEGER NOT NULL DEFAULT 0"}, {name: "connection_id", definition: "TEXT NOT NULL DEFAULT ''"}},
		"usage_totals":    {{name: "estimated_cost_micros", definition: "INTEGER NOT NULL DEFAULT 0"}},
	} {
		for _, column := range columns {
			if err := ensureColumn(db, table, column.name, column.definition); err != nil {
				return err
			}
		}
	}
	modelColumns := []struct {
		name       string
		definition string
	}{
		{name: "cooldown_until", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "failure_count", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "error_rate", definition: "REAL NOT NULL DEFAULT 0"},
		{name: "last_health_check", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "catalog_source", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "discovered_at", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "verified_at", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "verification_error", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "effort_json", definition: "TEXT NOT NULL DEFAULT '[]'"},
		{name: "token_cost", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "context_window", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "max_output", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "thinking", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "vision", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "tool_use", definition: "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, column := range modelColumns {
		if err := ensureColumn(db, "model_catalog", column.name, column.definition); err != nil {
			return err
		}
	}
	return nil
}

func ensureColumn(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return fmt.Errorf("inspect sqlite columns: %w", err)
	}
	defer rows.Close()
	var found bool
	for rows.Next() {
		var cid int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("read sqlite columns: %w", err)
		}
		if name == column {
			found = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sqlite columns: %w", err)
	}
	if found {
		return nil
	}
	if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition); err != nil {
		return fmt.Errorf("add sqlite column %s: %w", column, err)
	}
	return nil
}

func (s *Store) RecordRequest(event RequestEvent) bool {
	return s.EnqueueRequest(event) == nil
}

func (s *Store) EnqueueRequest(event RequestEvent) error {
	if s == nil {
		return ErrStoreClosed
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return ErrStoreClosed
	}
	if event.RequestID == "" {
		event.RequestID = fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	select {
	case s.events <- event:
		s.queued.Add(1)
		return nil
	default:
		s.dropped.Add(1)
		observability.Logger("storage").Warn("storage_queue_full", "request_id", event.RequestID)
		return ErrQueueFull
	}
}

func (s *Store) writeLoop() {
	defer s.wg.Done()
	for {
		select {
		case event := <-s.events:
			s.persist(event)
		case <-s.done:
			for {
				select {
				case event := <-s.events:
					s.persist(event)
				default:
					return
				}
			}
		}
	}
}

func (s *Store) persist(event RequestEvent) {
	if err := s.insertRequest(event); err != nil {
		observability.Logger("storage").Error("storage_write_failed", "request_id", event.RequestID, "error", observability.PublicError(err))
		s.writeErrs.Add(1)
		s.statsMu.Lock()
		s.lastErr = err
		hook := s.errorHook
		s.statsMu.Unlock()
		if hook != nil {
			hook(err)
		}
		return
	}
	s.written.Add(1)
}

func (s *Store) insertRequest(event RequestEvent) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}
	totalTokens := event.PromptTokens + event.CompletionTokens
	decisionJSON := strings.TrimSpace(event.DecisionJSON)
	if decisionJSON == "" {
		decisionJSON = "{}"
	}
	_, err = tx.Exec(`INSERT OR REPLACE INTO request_history
		 (request_id, client, endpoint, connection_id, provider_id, model_id, status, fallback, latency_ms, prompt_tokens, completion_tokens, total_tokens, cost_micros, decision_json, started_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.RequestID, event.Client, event.Endpoint, event.ConnectionID,
		event.Provider, event.Model, event.Status, boolInt(event.Fallback), event.LatencyMS, event.PromptTokens, event.CompletionTokens, totalTokens, event.CostMicros, decisionJSON, event.At.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return rollback(err)
	}
	_, err = tx.Exec(`INSERT INTO usage_totals(provider_id, model_id, requests, failures, estimated_tokens, estimated_cost_micros, updated_at)
		 VALUES (?, ?, 1, ?, ?, ?, ?)
		 ON CONFLICT(provider_id, model_id) DO UPDATE SET
		 requests=requests+1, failures=failures+excluded.failures, estimated_tokens=estimated_tokens+excluded.estimated_tokens, estimated_cost_micros=estimated_cost_micros+excluded.estimated_cost_micros, updated_at=excluded.updated_at`,
		event.Provider, event.Model, boolInt(event.Status != "ok"), totalTokens, event.CostMicros, event.At.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return rollback(err)
	}
	for i, attempt := range event.Attempts {
		startedAt := attempt.StartedAt
		if startedAt.IsZero() {
			startedAt = event.At
		}
		if _, err := tx.Exec(`INSERT INTO attempt_history(request_id, attempt_number, provider_id, model_id, connection_id, status, latency_ms, error_redacted, started_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.RequestID, i+1, attempt.ProviderID, attempt.ModelID, attempt.ConnectionID, attempt.Status, attempt.LatencyMS, attempt.Error, startedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return rollback(err)
		}
	}
	return tx.Commit()
}

func (s *Store) ReplaceCatalog(providers []ProviderRecord, models []ModelRecord) error {
	if s == nil {
		return ErrStoreClosed
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return ErrStoreClosed
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin catalog transaction: %w", err)
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}
	if _, err := tx.Exec(`DELETE FROM model_catalog`); err != nil {
		return rollback(fmt.Errorf("clear model catalog: %w", err))
	}
	if _, err := tx.Exec(`DELETE FROM provider_catalog`); err != nil {
		return rollback(fmt.Errorf("clear provider catalog: %w", err))
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, provider := range providers {
		if _, err := tx.Exec(`INSERT INTO provider_catalog(provider_id, cli_type, executable, auth_state, updated_at) VALUES (?, ?, ?, ?, ?)`, provider.ProviderID, provider.CLIType, provider.Executable, provider.AuthState, now); err != nil {
			return rollback(fmt.Errorf("write provider catalog: %w", err))
		}
	}
	for _, model := range models {
		capabilities, err := json.Marshal(model.Capabilities)
		if err != nil {
			return rollback(fmt.Errorf("encode model capabilities: %w", err))
		}
		slots, err := json.Marshal(model.Slots)
		if err != nil {
			return rollback(fmt.Errorf("encode model slots: %w", err))
		}
		effort, err := json.Marshal(model.Effort)
		if err != nil {
			return rollback(fmt.Errorf("encode model effort: %w", err))
		}
		cooldownUntil := ""
		if !model.CooldownUntil.IsZero() {
			cooldownUntil = model.CooldownUntil.UTC().Format(time.RFC3339Nano)
		}
		lastHealthCheck := ""
		if !model.LastHealthCheck.IsZero() {
			lastHealthCheck = model.LastHealthCheck.UTC().Format(time.RFC3339Nano)
		}
		discoveredAt := ""
		if !model.DiscoveredAt.IsZero() {
			discoveredAt = model.DiscoveredAt.UTC().Format(time.RFC3339Nano)
		}
		verifiedAt := ""
		if !model.VerifiedAt.IsZero() {
			verifiedAt = model.VerifiedAt.UTC().Format(time.RFC3339Nano)
		}
		if _, err := tx.Exec(`INSERT INTO model_catalog(model_id, provider_id, capabilities_json, slots_json, catalog_source, discovered_at, verified_at, verification_error, effort_json, health_state, cost_tier, token_cost, context_window, max_output, max_tokens, thinking, vision, tool_use, latency_p50_ms, latency_p95_ms, cooldown_until, failure_count, error_rate, last_health_check, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, model.ModelID, model.ProviderID, string(capabilities), string(slots), model.CatalogSource, discoveredAt, verifiedAt, model.VerificationErr, string(effort), model.HealthState, model.CostTier, model.TokenCost, model.ContextWindow, model.MaxOutput, model.MaxTokens, boolInt(model.Thinking), boolInt(model.Vision), boolInt(model.ToolUse), model.LatencyP50.Milliseconds(), model.LatencyP95.Milliseconds(), cooldownUntil, model.FailureCount, model.ErrorRate, lastHealthCheck, now); err != nil {
			return rollback(fmt.Errorf("write model catalog: %w", err))
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit catalog transaction: %w", err)
	}
	return nil
}

func (s *Store) LoadModelCatalog() ([]ModelRecord, error) {
	if s == nil {
		return nil, ErrStoreClosed
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return nil, ErrStoreClosed
	}
	rows, err := s.db.Query(`SELECT model_id, provider_id, capabilities_json, slots_json, catalog_source, discovered_at, verified_at, verification_error, effort_json, health_state, cost_tier, token_cost, context_window, max_output, max_tokens, thinking, vision, tool_use, latency_p50_ms, latency_p95_ms, cooldown_until, failure_count, error_rate, last_health_check FROM model_catalog`)
	if err != nil {
		return nil, fmt.Errorf("read model catalog: %w", err)
	}
	defer rows.Close()
	var records []ModelRecord
	for rows.Next() {
		var record ModelRecord
		var capabilities, slots, effort, discoveredAt, verifiedAt, cooldownUntil, lastHealthCheck string
		var thinking, vision, toolUse int
		var latencyP50MS, latencyP95MS int64
		if err := rows.Scan(&record.ModelID, &record.ProviderID, &capabilities, &slots, &record.CatalogSource, &discoveredAt, &verifiedAt, &record.VerificationErr, &effort, &record.HealthState, &record.CostTier, &record.TokenCost, &record.ContextWindow, &record.MaxOutput, &record.MaxTokens, &thinking, &vision, &toolUse, &latencyP50MS, &latencyP95MS, &cooldownUntil, &record.FailureCount, &record.ErrorRate, &lastHealthCheck); err != nil {
			return nil, fmt.Errorf("scan model catalog: %w", err)
		}
		if err := json.Unmarshal([]byte(capabilities), &record.Capabilities); err != nil {
			return nil, fmt.Errorf("decode model capabilities: %w", err)
		}
		if err := json.Unmarshal([]byte(slots), &record.Slots); err != nil {
			return nil, fmt.Errorf("decode model slots: %w", err)
		}
		if err := json.Unmarshal([]byte(effort), &record.Effort); err != nil {
			return nil, fmt.Errorf("decode model effort: %w", err)
		}
		if discoveredAt != "" {
			record.DiscoveredAt, err = time.Parse(time.RFC3339Nano, discoveredAt)
			if err != nil {
				return nil, fmt.Errorf("parse model discovery timestamp: %w", err)
			}
		}
		if verifiedAt != "" {
			record.VerifiedAt, err = time.Parse(time.RFC3339Nano, verifiedAt)
			if err != nil {
				return nil, fmt.Errorf("parse model verification timestamp: %w", err)
			}
		}
		record.Thinking = thinking != 0
		record.Vision = vision != 0
		record.ToolUse = toolUse != 0
		record.LatencyP50 = time.Duration(latencyP50MS) * time.Millisecond
		record.LatencyP95 = time.Duration(latencyP95MS) * time.Millisecond
		if cooldownUntil != "" {
			record.CooldownUntil, err = time.Parse(time.RFC3339Nano, cooldownUntil)
			if err != nil {
				return nil, fmt.Errorf("parse model cooldown: %w", err)
			}
		}
		if lastHealthCheck != "" {
			record.LastHealthCheck, err = time.Parse(time.RFC3339Nano, lastHealthCheck)
			if err != nil {
				return nil, fmt.Errorf("parse model health timestamp: %w", err)
			}
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate model catalog: %w", err)
	}
	return records, nil
}

func (s *Store) ReplaceControlPlane(connections []ConnectionRecord, pools []PoolRecord, combos []ComboRecord) error {
	if s == nil {
		return ErrStoreClosed
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return ErrStoreClosed
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin control-plane transaction: %w", err)
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}
	for _, table := range []string{"connections", "pools", "combos"} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return rollback(fmt.Errorf("clear %s: %w", table, err))
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, connection := range connections {
		metadata, err := json.Marshal(connection.Metadata)
		if err != nil {
			return rollback(fmt.Errorf("encode connection metadata: %w", err))
		}
		if _, err := tx.Exec(`INSERT INTO connections(name, provider_id, model_id, enabled, metadata_json, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, connection.Name, connection.Provider, connection.Model, boolInt(connection.Enabled), string(metadata), now); err != nil {
			return rollback(fmt.Errorf("write connection: %w", err))
		}
	}
	for _, pool := range pools {
		members, err := json.Marshal(pool.Members)
		if err != nil {
			return rollback(fmt.Errorf("encode pool members: %w", err))
		}
		if _, err := tx.Exec(`INSERT INTO pools(name, members_json, strategy, enabled, updated_at) VALUES (?, ?, ?, ?, ?)`, pool.Name, string(members), pool.Strategy, boolInt(pool.Enabled), now); err != nil {
			return rollback(fmt.Errorf("write pool: %w", err))
		}
	}
	for _, combo := range combos {
		members, err := json.Marshal(combo.Members)
		if err != nil {
			return rollback(fmt.Errorf("encode combo members: %w", err))
		}
		if _, err := tx.Exec(`INSERT INTO combos(name, members_json, strategy, judge, enabled, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, combo.Name, string(members), combo.Strategy, combo.Judge, boolInt(combo.Enabled), now); err != nil {
			return rollback(fmt.Errorf("write combo: %w", err))
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit control-plane transaction: %w", err)
	}
	return nil
}

func (s *Store) RecordHealthSample(sample HealthSample) error {
	if s == nil {
		return ErrStoreClosed
	}
	if sample.ObservedAt.IsZero() {
		sample.ObservedAt = time.Now().UTC()
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return ErrStoreClosed
	}
	_, err := s.db.Exec(`INSERT INTO health_samples(provider_id, status, latency_ms, error_redacted, observed_at) VALUES (?, ?, ?, ?, ?)`, sample.ProviderID, sample.Status, sample.LatencyMS, sample.Error, sample.ObservedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("write health sample: %w", err)
	}
	return nil
}

func (s *Store) RecordConfigSnapshot(snapshot ConfigSnapshot) error {
	if s == nil {
		return ErrStoreClosed
	}
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = time.Now().UTC()
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return ErrStoreClosed
	}
	_, err := s.db.Exec(`INSERT INTO config_snapshots(checksum, path, created_at) VALUES (?, ?, ?)`, snapshot.Checksum, snapshot.Path, snapshot.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("write config snapshot: %w", err)
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) RecordAudit(action, actor string, details any) error {
	if s == nil {
		return ErrStoreClosed
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closed {
		return ErrStoreClosed
	}
	data, err := redactAuditDetails(details)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO audit_events(action, actor, details_redacted, created_at) VALUES (?, ?, ?, ?)`, action, actor, string(data), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListAudit(limit int) ([]AuditEvent, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT action, actor, details_redacted, created_at FROM audit_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	events := make([]AuditEvent, 0)
	for rows.Next() {
		var event AuditEvent
		var created string
		if err := rows.Scan(&event.Action, &event.Actor, &event.Details, &created); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		if event.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("parse audit timestamp: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	return events, nil
}

func redactAuditDetails(details any) ([]byte, error) {
	raw, err := json.Marshal(details)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(redactAuditValue(value, false))
}

func redactAuditValue(value any, sensitive bool) any {
	switch current := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(current))
		for key, item := range current {
			out[key] = redactAuditValue(item, auditKeyIsSensitive(key))
		}
		return out
	case []any:
		out := make([]any, len(current))
		for i, item := range current {
			out[i] = redactAuditValue(item, sensitive)
		}
		return out
	case string:
		if sensitive {
			return "[redacted]"
		}
	}
	return value
}

func auditKeyIsSensitive(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, marker := range []string{"token", "secret", "password", "authorization", "api_key", "apikey", "credential", "prompt", "content", "argument"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func (s *Store) SetWriteErrorHook(hook func(error)) {
	if s == nil {
		return
	}
	s.statsMu.Lock()
	s.errorHook = hook
	s.statsMu.Unlock()
}

func (s *Store) Stats() Stats {
	if s == nil {
		return Stats{Closed: true}
	}
	s.lifecycleMu.Lock()
	closed := s.closed
	depth := len(s.events)
	s.lifecycleMu.Unlock()
	s.statsMu.RLock()
	lastErr := ""
	if s.lastErr != nil {
		lastErr = s.lastErr.Error()
	}
	s.statsMu.RUnlock()
	return Stats{
		Queued: s.queued.Load(), Dropped: s.dropped.Load(), Written: s.written.Load(),
		WriteErrors: s.writeErrs.Load(), QueueDepth: depth, Closed: closed, LastError: lastErr,
	}
}

func (s *Store) Path() string {
	if s == nil || s.db == nil {
		return ""
	}
	return s.path
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.lifecycleMu.Lock()
		s.closed = true
		close(s.done)
		s.lifecycleMu.Unlock()
		s.wg.Wait()
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}
