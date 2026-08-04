package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"ghrouter/internal/types"
)

type genericACPWarmPool struct {
	mu      sync.Mutex
	process *genericACPProcess
}

type genericACPProcess struct {
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	stdin     io.WriteCloser
	lines     chan string
	wait      <-chan error
	client    *cursorACPClient
	nextID    int
	closeOnce sync.Once
}

func (p *genericACPWarmPool) do(ctx context.Context, runner *ProviderRunner, req *types.OpenAIRequest, eventCh chan<- *StreamEvent, emitted *bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	process, err := p.ensure(ctx, runner)
	if err != nil {
		return err
	}
	err = process.request(ctx, runner, req, eventCh, emitted)
	if isFatalACPProcessError(err) {
		process.close()
		if p.process == process {
			p.process = nil
		}
	}
	return err
}

func (p *genericACPWarmPool) ensure(ctx context.Context, runner *ProviderRunner) (*genericACPProcess, error) {
	if p.process != nil {
		return p.process, nil
	}
	process, err := startGenericACPProcess(ctx, runner)
	if err != nil {
		return nil, err
	}
	p.process = process
	return process, nil
}

func (p *genericACPWarmPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.process != nil {
		p.process.close()
		p.process = nil
	}
}

func startGenericACPProcess(ctx context.Context, runner *ProviderRunner) (*genericACPProcess, error) {
	processCtx, cancel := context.WithCancel(context.Background())
	args := []string{"acp"}
	if hasFlag(runner.prov.Args, "--pure") {
		args = append(args, "--pure")
	}
	cmd := exec.CommandContext(processCtx, runner.prov.CLIPath, args...)
	prepareProviderCommand(cmd)
	cmd.Dir = runner.prov.WorkDir
	cmd.Env = runner.buildEnv()
	cmd.WaitDelay = 500 * time.Millisecond
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s warm ACP stdin unavailable: %w", runner.prov.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		return nil, fmt.Errorf("%s warm ACP stdout unavailable: %w", runner.prov.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		cancel()
		return nil, fmt.Errorf("%s warm ACP stderr unavailable: %w", runner.prov.Name, err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		cancel()
		return nil, fmt.Errorf("%s warm ACP failed to start: %w", runner.prov.Name, err)
	}
	lines := make(chan string, 64)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		defer close(lines)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-processCtx.Done():
				return
			}
		}
	}()
	go func() { _, _ = io.Copy(io.Discard, stderr) }()
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	process := &genericACPProcess{cmd: cmd, cancel: cancel, stdin: stdin, lines: lines, wait: wait, nextID: 2}
	initCtx, initCancel := context.WithTimeout(ctx, 10*time.Second)
	defer initCancel()
	process.client = &cursorACPClient{ctx: initCtx, stdin: stdin, lines: lines}
	initialize, err := process.client.call(1, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": true, "writeTextFile": true},
			"terminal": true,
		},
		"clientInfo": map[string]string{"name": "ghrouter", "version": "dev"},
	})
	if err != nil {
		process.close()
		return nil, fmt.Errorf("%s warm ACP initialize failed: %w", runner.prov.Name, err)
	}
	var result genericACPInitializeResult
	if err := json.Unmarshal(initialize.Result, &result); err != nil || result.ProtocolVersion == 0 {
		process.close()
		return nil, fmt.Errorf("%s warm ACP initialize returned an invalid response", runner.prov.Name)
	}
	if err := process.client.notify("initialized", map[string]any{}); err != nil {
		process.close()
		return nil, fmt.Errorf("%s warm ACP initialization acknowledgement failed: %w", runner.prov.Name, err)
	}
	process.client.ctx = processCtx
	return process, nil
}

func (p *genericACPProcess) request(ctx context.Context, runner *ProviderRunner, req *types.OpenAIRequest, eventCh chan<- *StreamEvent, emitted *bool) error {
	var providerErr error
	p.client.ctx = ctx
	p.client.update = func(message cursorACPMessage) {
		var params struct {
			Update struct {
				Content json.RawMessage `json:"content"`
			} `json:"update"`
		}
		if json.Unmarshal(message.Params, &params) != nil {
			return
		}
		for _, text := range genericACPTextChunks(params.Update.Content) {
			if text == "" {
				continue
			}
			if err := providerOutputError(text); err != nil {
				if providerErr == nil {
					providerErr = err
				}
				continue
			}
			select {
			case eventCh <- &StreamEvent{ID: fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()), Model: req.Model, Delta: text}:
				*emitted = true
			case <-ctx.Done():
			}
		}
	}
	workDir := runner.prov.WorkDir
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	newSession, err := p.call(ctx, "session/new", map[string]any{"cwd": workDir, "mcpServers": []any{}})
	if err != nil {
		return fmt.Errorf("%s warm ACP session creation failed: %w", runner.prov.Name, err)
	}
	var session genericACPSessionResult
	if err := json.Unmarshal(newSession.Result, &session); err != nil || session.SessionID == "" {
		return fmt.Errorf("%s warm ACP session/new returned an invalid response", runner.prov.Name)
	}
	nativeModel := nativeModelID(adapterFor(runner.prov.Type).Name(), req.Model)
	if modelOption, ok := genericACPModelOption(session.ConfigOptions); ok && nativeModel != "" && !strings.EqualFold(nativeModel, "auto") {
		if !genericACPModelAvailable(modelOption, nativeModel) {
			return fmt.Errorf("%s ACP model %q is not in the installed catalog", runner.prov.Name, nativeModel)
		}
		if _, err := p.call(ctx, "session/set_config_option", map[string]string{"sessionId": session.SessionID, "configId": modelOption.ID, "value": nativeModel}); err != nil {
			return fmt.Errorf("%s warm ACP model selection failed: %w", runner.prov.Name, err)
		}
	}
	if effortOption, ok := genericACPEffortOption(session.ConfigOptions); ok && req.ReasoningEffort != "" {
		wanted := strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
		if genericACPOptionAvailable(effortOption, wanted) {
			if _, err := p.call(ctx, "session/set_config_option", map[string]string{"sessionId": session.SessionID, "configId": effortOption.ID, "value": wanted}); err != nil {
				return fmt.Errorf("%s warm ACP reasoning effort selection failed: %w", runner.prov.Name, err)
			}
		}
	}
	prompt, err := p.call(ctx, "session/prompt", map[string]any{
		"sessionId": session.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": runner.buildPrompt(req)}},
	})
	if err != nil {
		return fmt.Errorf("%s warm ACP prompt failed: %w", runner.prov.Name, err)
	}
	if providerErr != nil {
		return providerErr
	}
	var promptResult struct {
		StopReason string `json:"stopReason"`
	}
	if json.Unmarshal(prompt.Result, &promptResult) == nil && promptResult.StopReason == "error" {
		return fmt.Errorf("%s warm ACP prompt stopped with an error", runner.prov.Name)
	}
	if !*emitted {
		return &EmptyResponseError{Provider: runner.prov.Name}
	}
	return nil
}

func (p *genericACPProcess) call(ctx context.Context, method string, params any) (cursorACPMessage, error) {
	p.client.ctx = ctx
	id := p.nextID
	p.nextID++
	return p.client.call(id, method, params)
}

func (p *genericACPProcess) close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		p.cancel()
		_ = p.stdin.Close()
		killProviderProcess(p.cmd)
		select {
		case <-p.wait:
		case <-time.After(time.Second):
		}
	})
}

func isFatalACPProcessError(err error) bool {
	return err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF))
}
