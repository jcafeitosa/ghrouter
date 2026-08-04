package providers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"ghrouter/internal/types"
)

type genericACPInitializeResult struct {
	ProtocolVersion int `json:"protocolVersion"`
}

type genericACPSessionResult struct {
	SessionID     string                   `json:"sessionId"`
	ConfigOptions []genericACPConfigOption `json:"configOptions"`
}

type genericACPConfigOption struct {
	ID           string             `json:"id"`
	CurrentValue string             `json:"currentValue"`
	Options      []genericACPOption `json:"options"`
}

type genericACPOption struct {
	Value string `json:"value"`
}

func (r *ProviderRunner) executeGenericACP(ctx context.Context, req *types.OpenAIRequest, eventCh chan<- *StreamEvent, emitted *bool) error {
	commandCtx := ctx
	if r.prov.Timeout > 0 {
		var cancel context.CancelFunc
		commandCtx, cancel = context.WithTimeout(ctx, r.prov.Timeout)
		defer cancel()
	}

	args := []string{"acp"}
	if hasFlag(r.prov.Args, "--pure") {
		args = append(args, "--pure")
	}
	cmd := exec.CommandContext(commandCtx, r.prov.CLIPath, args...)
	prepareProviderCommand(cmd)
	cmd.Dir = r.prov.WorkDir
	cmd.Env = r.buildEnv()
	cmd.WaitDelay = 500 * time.Millisecond
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("%s ACP stdin unavailable: %w", r.prov.Name, err)
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("%s ACP stdout unavailable: %w", r.prov.Name, err)
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		return fmt.Errorf("%s ACP stderr unavailable: %w", r.prov.Name, err)
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		return fmt.Errorf("%s ACP failed to start: %w", r.prov.Name, err)
	}
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	stopKill := make(chan struct{})
	go func() {
		select {
		case <-commandCtx.Done():
			killProviderProcess(cmd)
			_ = stdout.Close()
			_ = stderr.Close()
		case <-stopKill:
		}
	}()
	defer func() {
		close(stopKill)
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		killProviderProcess(cmd)
		select {
		case <-waitCh:
		case <-time.After(time.Second):
		}
	}()

	go func() {
		_, _ = io.ReadAll(stderr)
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lines := make(chan string)
	scanErrors := make(chan error, 1)
	go func() {
		defer close(lines)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-commandCtx.Done():
				return
			}
		}
		scanErrors <- scanner.Err()
	}()

	var providerErr error
	client := &cursorACPClient{ctx: commandCtx, stdin: stdin, lines: lines}
	client.update = func(message cursorACPMessage) {
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
			case <-commandCtx.Done():
				return
			}
		}
	}

	initialize, err := client.call(1, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": true, "writeTextFile": true},
			"terminal": true,
		},
		"clientInfo": map[string]string{"name": "ghrouter", "version": "dev"},
	})
	if err != nil {
		return fmt.Errorf("%s ACP initialize failed: %w", r.prov.Name, err)
	}
	var initializeResult genericACPInitializeResult
	if err := json.Unmarshal(initialize.Result, &initializeResult); err != nil || initializeResult.ProtocolVersion == 0 {
		return fmt.Errorf("%s ACP initialize returned an invalid response", r.prov.Name)
	}
	if err := client.notify("initialized", map[string]any{}); err != nil {
		return fmt.Errorf("%s ACP initialization acknowledgement failed: %w", r.prov.Name, err)
	}

	workDir := r.prov.WorkDir
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	newSession, err := client.call(2, "session/new", map[string]any{"cwd": workDir, "mcpServers": []any{}})
	if err != nil {
		return fmt.Errorf("%s ACP session creation failed: %w", r.prov.Name, err)
	}
	var session genericACPSessionResult
	if err := json.Unmarshal(newSession.Result, &session); err != nil || session.SessionID == "" {
		return fmt.Errorf("%s ACP session/new returned an invalid response", r.prov.Name)
	}

	nativeModel := nativeModelID(adapterFor(r.prov.Type).Name(), req.Model)
	nextRequestID := 3
	if modelOption, ok := genericACPModelOption(session.ConfigOptions); ok && nativeModel != "" && !strings.EqualFold(nativeModel, "auto") {
		if !genericACPModelAvailable(modelOption, nativeModel) {
			return fmt.Errorf("%s ACP model %q is not in the installed catalog", r.prov.Name, nativeModel)
		}
		if _, err := client.call(nextRequestID, "session/set_config_option", map[string]string{"sessionId": session.SessionID, "configId": modelOption.ID, "value": nativeModel}); err != nil {
			return fmt.Errorf("%s ACP model selection failed: %w", r.prov.Name, err)
		}
		nextRequestID++
	}
	if effortOption, ok := genericACPEffortOption(session.ConfigOptions); ok && req.ReasoningEffort != "" {
		wanted := strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
		if genericACPOptionAvailable(effortOption, wanted) {
			if _, err := client.call(nextRequestID, "session/set_config_option", map[string]string{"sessionId": session.SessionID, "configId": effortOption.ID, "value": wanted}); err != nil {
				return fmt.Errorf("%s ACP reasoning effort selection failed: %w", r.prov.Name, err)
			}
			nextRequestID++
		}
	}

	prompt, err := client.call(nextRequestID, "session/prompt", map[string]any{
		"sessionId": session.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": r.buildPrompt(req)}},
	})
	if err != nil {
		return fmt.Errorf("%s ACP prompt failed: %w", r.prov.Name, err)
	}
	if providerErr != nil {
		return providerErr
	}
	select {
	case scanErr := <-scanErrors:
		if scanErr != nil {
			return fmt.Errorf("%s ACP output scan failed: %w", r.prov.Name, scanErr)
		}
	default:
	}
	var promptResult struct {
		StopReason string `json:"stopReason"`
	}
	if json.Unmarshal(prompt.Result, &promptResult) == nil && promptResult.StopReason == "error" {
		return fmt.Errorf("%s ACP prompt stopped with an error", r.prov.Name)
	}
	if !*emitted {
		return &EmptyResponseError{Provider: r.prov.Name}
	}
	return nil
}

func genericACPModelOption(options []genericACPConfigOption) (genericACPConfigOption, bool) {
	for _, option := range options {
		if option.ID == "model" {
			return option, true
		}
	}
	return genericACPConfigOption{}, false
}

func genericACPEffortOption(options []genericACPConfigOption) (genericACPConfigOption, bool) {
	for _, option := range options {
		id := strings.ToLower(option.ID)
		if strings.Contains(id, "effort") || strings.Contains(id, "thinking") || strings.Contains(id, "variant") || strings.Contains(id, "reasoning") {
			return option, true
		}
	}
	return genericACPConfigOption{}, false
}

func genericACPModelAvailable(option genericACPConfigOption, wanted string) bool {
	return genericACPOptionAvailable(option, wanted)
}

func genericACPOptionAvailable(option genericACPConfigOption, wanted string) bool {
	for _, candidate := range option.Options {
		if strings.EqualFold(candidate.Value, wanted) {
			return true
		}
	}
	return strings.EqualFold(option.CurrentValue, wanted)
}

func genericACPTextChunks(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var object struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &object) == nil && object.Text != "" {
		return []string{object.Text}
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return nil
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return texts
}
