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

type cursorACPClient struct {
	ctx    context.Context
	stdin  io.Writer
	lines  <-chan string
	update func(cursorACPMessage)
}

func (r *ProviderRunner) executeCursorACP(ctx context.Context, req *types.OpenAIRequest, eventCh chan<- *StreamEvent, emitted *bool) error {
	commandCtx := ctx
	if r.prov.Timeout > 0 {
		var cancel context.CancelFunc
		commandCtx, cancel = context.WithTimeout(ctx, r.prov.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(commandCtx, r.prov.CLIPath, "agent", "--trust", "acp")
	prepareProviderCommand(cmd)
	cmd.Dir = r.prov.WorkDir
	cmd.Env = r.buildEnv()
	cmd.WaitDelay = 500 * time.Millisecond
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("cursor ACP stdin unavailable: %w", err)
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("cursor ACP stdout unavailable: %w", err)
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		return fmt.Errorf("cursor ACP stderr unavailable: %w", err)
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		return fmt.Errorf("cursor ACP failed to start: %w", err)
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
	stderrCh := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(stderr)
		stderrCh <- data
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
		var params cursorACPUpdateParams
		if json.Unmarshal(message.Params, &params) != nil || params.Update.Content.Type != "text" || params.Update.Content.Text == "" {
			return
		}
		if err := providerOutputError(params.Update.Content.Text); err != nil {
			if providerErr == nil {
				providerErr = err
			}
			return
		}
		select {
		case eventCh <- &StreamEvent{ID: fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()), Model: req.Model, Delta: params.Update.Content.Text}:
		case <-commandCtx.Done():
		}
		if commandCtx.Err() != nil {
			return
		}
		*emitted = true
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
		return fmt.Errorf("cursor ACP initialize failed: %w", err)
	}
	var initializeResult cursorACPInitializeResult
	if err := json.Unmarshal(initialize.Result, &initializeResult); err != nil || initializeResult.ProtocolVersion == 0 {
		return fmt.Errorf("cursor ACP initialize returned an invalid response")
	}
	if err := client.notify("initialized", map[string]any{}); err != nil {
		return fmt.Errorf("cursor ACP initialization acknowledgement failed: %w", err)
	}
	if len(initializeResult.AuthMethods) > 0 {
		if _, err := client.call(2, "authenticate", map[string]string{"methodId": initializeResult.AuthMethods[0].ID}); err != nil {
			return fmt.Errorf("cursor ACP authentication failed: %w", err)
		}
	}

	workDir := r.prov.WorkDir
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	newSession, err := client.call(3, "session/new", map[string]any{"cwd": workDir, "mcpServers": []any{}})
	if err != nil {
		return fmt.Errorf("cursor ACP session creation failed: %w", err)
	}
	var session cursorACPSessionResult
	if err := json.Unmarshal(newSession.Result, &session); err != nil || session.SessionID == "" {
		return fmt.Errorf("cursor ACP session/new returned an invalid response")
	}
	nextRequestID := 4
	if modelID, ok := cursorACPModelChoice(session.Models.AvailableModels, req.Model); ok {
		if _, err := client.call(nextRequestID, "session/set_config_option", map[string]string{"sessionId": session.SessionID, "configId": "model", "value": modelID}); err != nil {
			return fmt.Errorf("cursor ACP model selection failed: %w", err)
		}
		nextRequestID++
	}
	if effortOption, ok := genericACPEffortOption(session.ConfigOptions); ok && req.ReasoningEffort != "" {
		wanted := strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
		if genericACPOptionAvailable(effortOption, wanted) {
			if _, err := client.call(nextRequestID, "session/set_config_option", map[string]string{"sessionId": session.SessionID, "configId": effortOption.ID, "value": wanted}); err != nil {
				return fmt.Errorf("cursor ACP reasoning effort selection failed: %w", err)
			}
			nextRequestID++
		}
	}
	prompt, err := client.call(nextRequestID, "session/prompt", map[string]any{
		"sessionId": session.SessionID,
		"prompt":    []map[string]string{{"type": "text", "text": r.buildPrompt(req)}},
	})
	if err != nil {
		return fmt.Errorf("cursor ACP prompt failed: %w", err)
	}
	if providerErr != nil {
		return providerErr
	}
	select {
	case scanErr := <-scanErrors:
		if scanErr != nil {
			return fmt.Errorf("cursor ACP output scan failed: %w", scanErr)
		}
	default:
	}
	var promptResult cursorACPPromptResult
	if err := json.Unmarshal(prompt.Result, &promptResult); err == nil && promptResult.StopReason == "error" {
		return fmt.Errorf("cursor ACP prompt stopped with an error")
	}
	if !*emitted {
		return &EmptyResponseError{Provider: r.prov.Name}
	}
	return nil
}

func (c *cursorACPClient) call(id int, method string, params any) (cursorACPMessage, error) {
	if err := writeCursorACP(c.stdin, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return cursorACPMessage{}, err
	}
	for {
		var line string
		select {
		case <-c.ctx.Done():
			return cursorACPMessage{}, c.ctx.Err()
		case next, ok := <-c.lines:
			if !ok {
				return cursorACPMessage{}, io.EOF
			}
			line = next
		}
		var message cursorACPMessage
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			continue
		}
		if message.Method != "" {
			if message.ID != nil {
				_ = writeCursorACP(c.stdin, cursorACPPermissionResponse(message))
			}
			if c.update != nil {
				c.update(message)
			}
			continue
		}
		if !cursorACPIDMatches(message.ID, id) {
			continue
		}
		if message.Error != nil {
			return cursorACPMessage{}, message.Error
		}
		return message, nil
	}
}

func (c *cursorACPClient) notify(method string, params any) error {
	return writeCursorACP(c.stdin, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}
