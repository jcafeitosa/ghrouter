package local_brain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"ghrouter/internal/types"
)

type RuntimeStatus struct {
	Backend   BackendType
	Model     string
	ModelPath string
	Host      string
	Port      int
	URL       string
	Ready     bool
}

type managedProcess interface {
	Wait() error
	Kill() error
}

type processLiveness interface {
	Alive() bool
}

type runtimeDeps struct {
	detector BackendAvailability
	models   ModelPresence
	runner   ProvisionRunner
	start    func(context.Context, string, []string) (managedProcess, error)
	probe    func(context.Context, string) error
	warm     func(context.Context, string, string) error
}

type backendDetector interface {
	BackendAvailability
	Detect() (BackendType, error)
}

type Supervisor struct {
	mu          sync.RWMutex
	cfg         types.LocalBrainConfig
	deps        runtimeDeps
	process     managedProcess
	done        chan error
	ctx         context.Context
	backendType BackendType
	modelPath   string
	host        string
	port        int
	stopped     bool
	status      RuntimeStatus
}

func NewSupervisor(cfg types.LocalBrainConfig) (*Supervisor, error) {
	manager, err := NewModelManager()
	if err != nil {
		return nil, err
	}
	return newSupervisorWithDeps(cfg, runtimeDeps{
		detector: &Detector{},
		models:   manager,
		runner:   osProvisionRunner{},
		start:    startRuntimeProcess,
		probe:    probeRuntime,
		warm:     warmRuntime,
	}), nil
}

func newSupervisorWithDeps(cfg types.LocalBrainConfig, deps runtimeDeps) *Supervisor {
	if deps.runner == nil {
		deps.runner = osProvisionRunner{}
	}
	if deps.start == nil {
		deps.start = startRuntimeProcess
	}
	if deps.probe == nil {
		deps.probe = probeRuntime
	}
	return &Supervisor{cfg: cfg, deps: deps}
}

func (s *Supervisor) Start(ctx context.Context) (RuntimeStatus, error) {
	if s == nil {
		return RuntimeStatus{}, fmt.Errorf("local brain supervisor is nil")
	}
	if ctx == nil {
		return RuntimeStatus{}, fmt.Errorf("local brain context is nil")
	}
	if !s.cfg.Enabled {
		return RuntimeStatus{}, fmt.Errorf("local brain is disabled")
	}
	s.mu.RLock()
	running := s.process != nil
	s.mu.RUnlock()
	if running {
		return RuntimeStatus{}, fmt.Errorf("local brain is already running")
	}
	s.mu.Lock()
	s.stopped = false
	s.mu.Unlock()
	modelName := strings.TrimSpace(s.cfg.Model)
	if modelName == "" {
		modelName = strings.TrimSpace(s.cfg.Source)
	}
	if modelName == "" {
		return RuntimeStatus{}, fmt.Errorf("local brain model is not configured")
	}
	backend, err := s.backend(ctx)
	if err != nil {
		return RuntimeStatus{}, err
	}
	modelRef := strings.TrimSpace(s.cfg.Source)
	if modelRef == "" {
		modelRef = modelName
	}
	modelPath, err := s.prepareModel(ctx, backend, modelRef)
	if err != nil {
		return RuntimeStatus{}, err
	}
	host := strings.TrimSpace(s.cfg.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	port := s.cfg.Port
	if port <= 0 {
		port = 19090
	}
	args := launchArgs(backend, modelPath, host, port)
	if len(args) == 0 {
		return RuntimeStatus{}, fmt.Errorf("no launcher is configured for backend %s", backend)
	}
	process, err := s.deps.start(ctx, launcherName(backend, modelPath), args)
	if err != nil {
		return RuntimeStatus{}, fmt.Errorf("start local %s: %w", backend, err)
	}
	done := make(chan error, 1)
	s.mu.Lock()
	s.process = process
	s.done = done
	s.ctx = ctx
	s.backendType = backend
	s.modelPath = modelPath
	s.host = host
	s.port = port
	s.mu.Unlock()
	go s.monitor(process, done)
	url := "http://" + host + ":" + strconv.Itoa(port)
	startupTimeout := s.cfg.StartupTimeout
	if startupTimeout <= 0 {
		startupTimeout = 60 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	if err := s.deps.probe(probeCtx, url); err != nil {
		_ = s.Stop()
		return RuntimeStatus{}, fmt.Errorf("local brain readiness failed: %w", err)
	}
	if s.deps.warm != nil {
		if err := s.deps.warm(probeCtx, url, modelName); err != nil {
			_ = s.Stop()
			return RuntimeStatus{}, fmt.Errorf("local brain model warmup failed: %w", err)
		}
	}
	if liveness, ok := process.(processLiveness); ok && !liveness.Alive() {
		_ = s.Stop()
		return RuntimeStatus{}, fmt.Errorf("local brain process exited before readiness completed")
	}
	status := RuntimeStatus{Backend: backend, Model: modelName, ModelPath: modelPath, Host: host, Port: port, URL: url, Ready: true}
	s.mu.Lock()
	if s.process == nil {
		s.mu.Unlock()
		return RuntimeStatus{}, fmt.Errorf("local brain process exited before readiness completed")
	}
	s.status = status
	s.mu.Unlock()
	return status, nil
}

func warmRuntime(ctx context.Context, baseURL, model string) error {
	return VerifyText(ctx, baseURL, model)
}

func VerifyText(ctx context.Context, baseURL, model string) error {
	payload := map[string]any{
		"model":                model,
		"messages":             []map[string]string{{"role": "user", "content": "Reply with GHROUTER_MLX_READY."}},
		"max_tokens":           32,
		"stream":               false,
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	}
	return verifyTextRuntime(ctx, baseURL, payload)
}

func verifyTextRuntime(ctx context.Context, baseURL string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("warmup returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return err
	}
	if len(result.Choices) == 0 || strings.TrimSpace(result.Choices[0].Message.Content) == "" {
		return fmt.Errorf("local runtime returned empty text")
	}
	return nil
}

func VerifyTools(ctx context.Context, baseURL, model string) error {
	payload := map[string]any{
		"model":                model,
		"messages":             []map[string]string{{"role": "user", "content": "Call the ghrouter_brain_probe tool now."}},
		"tools":                []map[string]any{{"type": "function", "function": map[string]any{"name": "ghrouter_brain_probe", "description": "Readiness probe for local tool calling", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}},
		"tool_choice":          "required",
		"max_tokens":           32,
		"stream":               false,
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	}
	return verifyToolRuntime(ctx, baseURL, payload)
}

func verifyToolRuntime(ctx context.Context, baseURL string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("warmup returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content   string     `json:"content"`
				ToolCalls []struct{} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return err
	}
	if len(result.Choices) == 0 || len(result.Choices[0].Message.ToolCalls) == 0 {
		return fmt.Errorf("local runtime did not return a tool call")
	}
	return nil
}

func (s *Supervisor) Stop() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	process, done := s.process, s.done
	s.process = nil
	s.done = nil
	s.stopped = true
	s.status.Ready = false
	s.mu.Unlock()
	if process == nil {
		return nil
	}
	err := process.Kill()
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			if err == nil {
				err = fmt.Errorf("local brain process did not stop")
			}
		}
	}
	return err
}

func (s *Supervisor) Status() RuntimeStatus {
	if s == nil {
		return RuntimeStatus{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *Supervisor) monitor(process managedProcess, done chan<- error) {
	err := process.Wait()
	done <- err
	s.mu.Lock()
	if s.process == process {
		s.process = nil
		s.done = nil
		s.status.Ready = false
		ctx := s.ctx
		restart := s.cfg.Restart && !s.stopped
		backend, modelPath, host, port := s.backendType, s.modelPath, s.host, s.port
		s.mu.Unlock()
		if restart {
			s.restart(ctx, backend, modelPath, host, port)
		}
		return
	}
	s.mu.Unlock()
}

func (s *Supervisor) restart(ctx context.Context, backend BackendType, modelPath, host string, port int) {
	backoff := s.cfg.RestartBackoff
	if backoff <= 0 {
		backoff = time.Second
	}
	max := s.cfg.MaxRestarts
	if max <= 0 {
		max = 3
	}
	for attempt := 0; attempt < max; attempt++ {
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		process, err := s.deps.start(ctx, launcherName(backend, modelPath), launchArgs(backend, modelPath, host, port))
		if err != nil {
			continue
		}
		probeTimeout := s.cfg.StartupTimeout
		if probeTimeout <= 0 {
			probeTimeout = 60 * time.Second
		}
		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		probeErr := s.deps.probe(probeCtx, "http://"+host+":"+strconv.Itoa(port))
		cancel()
		if probeErr != nil {
			_ = process.Kill()
			_ = process.Wait()
			continue
		}
		done := make(chan error, 1)
		s.mu.Lock()
		if s.stopped || ctx.Err() != nil {
			s.mu.Unlock()
			_ = process.Kill()
			_ = process.Wait()
			return
		}
		s.process = process
		s.done = done
		s.status.Ready = true
		s.mu.Unlock()
		go s.monitor(process, done)
		return
	}
}

func (s *Supervisor) backend(ctx context.Context) (BackendType, error) {
	backend := BackendType(strings.TrimSpace(s.cfg.Backend))
	if backend == BackendNone || backend == "" {
		if detector, ok := s.deps.detector.(backendDetector); ok {
			detected, err := detector.Detect()
			if err != nil {
				return BackendNone, fmt.Errorf("detect local brain backend: %w", err)
			}
			if detected != BackendNone {
				return detected, nil
			}
		}
		backend = preferredBackendForHost()
	}
	if backend != BackendMLX && backend != BackendLLAMACPP {
		return BackendNone, fmt.Errorf("unsupported local brain backend %q", backend)
	}
	if s.deps.detector.IsBackendAvailable(backend) {
		return backend, nil
	}
	if strings.TrimSpace(s.cfg.Backend) == "" {
		fallback := BackendLLAMACPP
		if backend == BackendLLAMACPP {
			fallback = BackendMLX
		}
		if s.deps.detector.IsBackendAvailable(fallback) {
			return fallback, nil
		}
	}
	if !s.cfg.AutoProvision {
		return BackendNone, fmt.Errorf("%s backend unavailable; set local_brain.auto_provision=true to install it", backend)
	}
	action := ProvisionAction{Backend: backend, Action: "backend_setup", ApplyOK: true, Command: backendSetupCommand(backend)}
	if err := ExecuteProvisionPlan(ctx, []ProvisionAction{action}, s.deps.runner); err != nil {
		return BackendNone, fmt.Errorf("provision %s backend: %w", backend, err)
	}
	if !s.deps.detector.IsBackendAvailable(backend) {
		return BackendNone, fmt.Errorf("%s backend remains unavailable after provisioning", backend)
	}
	return backend, nil
}

func (s *Supervisor) prepareModel(ctx context.Context, backend BackendType, model string) (string, error) {
	manager, ok := s.deps.models.(*ModelManager)
	if !ok || manager == nil {
		if s.deps.models.HasModel(backend, model) {
			return model, nil
		}
		return "", fmt.Errorf("local model %q is not present", model)
	}
	path, err := manager.EnsureModelAvailable(backend, model)
	if err == nil {
		return path, nil
	}
	if !s.cfg.AutoProvision {
		return "", fmt.Errorf("local model %q is unavailable; set local_brain.auto_provision=true to download it", model)
	}
	source := modelSourceForBackend(backend, model)
	if source == "" {
		return "", fmt.Errorf("local model %q has no declared Hugging Face source", model)
	}
	action := ProvisionAction{Backend: backend, Model: model, Action: "model_cache", ApplyOK: true, Source: source, Command: modelDownloadCommand(backend, model)}
	if err := ExecuteProvisionPlan(ctx, []ProvisionAction{action}, s.deps.runner); err != nil {
		return "", fmt.Errorf("download local model %q: %w", model, err)
	}
	path, err = manager.EnsureModelAvailable(backend, model)
	if err != nil {
		return "", fmt.Errorf("local model %q remains unavailable after download: %w", model, err)
	}
	return path, nil
}

func launcherName(backend BackendType, modelPath string) string {
	if backend == BackendMLX {
		return "python3"
	}
	return "llama-server"
}

func launchArgs(backend BackendType, modelPath, host string, port int) []string {
	portText := strconv.Itoa(port)
	if backend == BackendMLX {
		return []string{"-m", "mlx_lm.server", "--model", modelPath, "--host", host, "--port", portText, "--chat-template-args", `{"enable_thinking":false}`}
	}
	if backend == BackendLLAMACPP {
		return []string{"-m", modelPath, "--host", host, "--port", portText}
	}
	return nil
}

func startRuntimeProcess(ctx context.Context, name string, args []string) (managedProcess, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	return osManagedProcess{command: command}, nil
}

type osManagedProcess struct {
	command *exec.Cmd
}

func (p osManagedProcess) Wait() error {
	return p.command.Wait()
}

func (p osManagedProcess) Kill() error {
	if p.command.Process == nil {
		return nil
	}
	return p.command.Process.Kill()
}

func (p osManagedProcess) Alive() bool {
	if p.command == nil || p.command.Process == nil || p.command.ProcessState != nil {
		return false
	}
	return p.command.Process.Signal(syscall.Signal(0)) == nil
}

func probeRuntime(ctx context.Context, baseURL string) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline, hasDeadline := ctx.Deadline()
	var occupiedSince time.Time
	for {
		if endpointAcceptsTCP(ctx, baseURL) {
			if occupiedSince.IsZero() {
				occupiedSince = time.Now()
			} else if time.Since(occupiedSince) >= 3*time.Second {
				return fmt.Errorf("endpoint %s is occupied but is not a responsive local brain", baseURL)
			}
		} else {
			occupiedSince = time.Time{}
		}
		for _, path := range []string{"/health", "/v1/models"} {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+path, nil)
			if err != nil {
				return err
			}
			response, err := client.Do(request)
			if err == nil {
				body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
				_ = response.Body.Close()
				if response.StatusCode >= 200 && response.StatusCode < 300 && looksLikeGhrouter(body) {
					return fmt.Errorf("endpoint %s is another ghrouter instance", baseURL)
				}
				if response.StatusCode >= 200 && response.StatusCode < 300 {
					return nil
				}
			}
		}
		if hasDeadline && time.Now().After(deadline) {
			return fmt.Errorf("endpoint %s did not become ready", baseURL)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func endpointAcceptsTCP(ctx context.Context, baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" {
		return false
	}
	dialCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", parsed.Host)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func looksLikeGhrouter(body []byte) bool {
	return bytes.Contains(body, []byte(`"provider_count"`)) ||
		bytes.Contains(body, []byte(`"ghrouter/auto"`)) ||
		bytes.Contains(body, []byte(`"owned_by":"ghrouter"`))
}

func Probe(ctx context.Context, baseURL string) error {
	if ctx == nil {
		return fmt.Errorf("local brain probe context is nil")
	}
	return probeRuntime(ctx, baseURL)
}
