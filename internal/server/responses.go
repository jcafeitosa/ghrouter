package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ghrouter/internal/providers"
	"ghrouter/internal/types"
)

type ResponsesRequest struct {
	Model           string          `json:"model"`
	RequestID       string          `json:"-"`
	Input           json.RawMessage `json:"input"`
	Instructions    string          `json:"instructions,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Tools           []ResponsesTool `json:"tools,omitempty"`
	ToolChoice      any             `json:"tool_choice,omitempty"`
}

type ResponsesTool struct {
	Type        string                `json:"type"`
	Name        string                `json:"name,omitempty"`
	Description string                `json:"description,omitempty"`
	Parameters  any                   `json:"parameters,omitempty"`
	Function    *types.OpenAIToolFunc `json:"function,omitempty"`
}

type ResponsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type ResponsesContent struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Annotations []any  `json:"annotations,omitempty"`
}

type ResponsesOutput struct {
	ID        string             `json:"id"`
	Type      string             `json:"type"`
	Role      string             `json:"role,omitempty"`
	Status    string             `json:"status,omitempty"`
	Content   []ResponsesContent `json:"content,omitempty"`
	CallID    string             `json:"call_id,omitempty"`
	Name      string             `json:"name,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
}

type ResponsesResponse struct {
	ID        string            `json:"id"`
	Object    string            `json:"object"`
	CreatedAt int64             `json:"created_at"`
	Status    string            `json:"status"`
	Model     string            `json:"model"`
	Output    []ResponsesOutput `json:"output"`
	Usage     ResponsesUsage    `json:"usage"`
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	started := time.Now()
	rid := requestID(r)
	end := s.telemetry.beginWithMeta(rid, requestClient(r))
	var request ResponsesRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		end("error", false, "", request.Model, "/v1/responses", time.Since(started))
		return
	}
	if len(bytes.TrimSpace(request.Input)) == 0 || bytes.Equal(bytes.TrimSpace(request.Input), []byte("null")) {
		writeError(w, http.StatusBadRequest, "invalid_request", "input must be provided")
		end("error", false, "", request.Model, "/v1/responses", time.Since(started))
		return
	}
	internal := responsesToOpenAIRequest(request)
	internal.RequestID = rid
	s.telemetry.recordDecision(rid, ProfileRequest(&internal))
	provider, model := s.RouteOpenAIRequest(&internal)
	s.telemetry.recordSelection(rid, provider, model, internal.SelectionStage, internal.SelectionReason)
	candidates := s.routeCandidates(request.Model, provider, model, &internal)
	if len(candidates) == 0 && isVirtualModelRequest(request.Model) {
		if s.verifyVirtualRouteOnDemand(r.Context(), request.Model, &internal) {
			provider, model = s.RouteOpenAIRequest(&internal)
			s.telemetry.recordSelection(rid, provider, model, internal.SelectionStage, internal.SelectionReason)
			candidates = s.routeCandidates(request.Model, provider, model, &internal)
		}
	}
	if len(candidates) == 0 {
		status := http.StatusNotFound
		code := "model_not_found"
		message := fmt.Sprintf("no provider for model %q", request.Model)
		if isVirtualModelRequest(request.Model) {
			status = http.StatusServiceUnavailable
			code = "model_unavailable"
			message = fmt.Sprintf("no verified provider is available for model %q; on-demand verification found no eligible provider", request.Model)
		}
		writeError(w, status, code, message)
		end("error", false, "", request.Model, "/v1/responses", time.Since(started))
		return
	}
	setRoutingHeaders(w, rid, request.Model, provider, model, internal.SelectionStage, len(candidates), internal.SelectionReason)
	if route := s.fusionRoute(request.Model); route != nil {
		s.handleFusionResponses(r.Context(), w, request, &internal, rid, end, started, candidates, route)
		return
	}
	if route := s.graphRoute(request.Model); route != nil {
		s.handleGraphResponses(r.Context(), w, request, &internal, rid, end, started, candidates, route)
		return
	}
	for index, candidate := range candidates {
		runner := s.getProvider(candidate.provider)
		if runner == nil {
			continue
		}
		internal.Model = candidate.model
		setRoutingHeaders(w, rid, request.Model, candidate.provider, candidate.model, internal.SelectionStage, len(candidates), internal.SelectionReason)
		attemptStarted := time.Now()
		if request.Stream {
			startedOutput, promptTokens, completionTokens, err := s.streamResponses(r.Context(), w, runner, &internal, candidate.model)
			status := "ok"
			if err != nil {
				status = "error"
			}
			s.telemetry.recordAttempt(rid, candidate.provider, candidate.model, status, publicProviderError(err), attemptStarted)
			if err == nil {
				s.telemetry.recordUsage(rid, promptTokens, completionTokens)
				s.catalog.RecordSuccess(candidate.provider+"/"+candidate.model, time.Now())
				end("ok", index > 0, candidate.provider, candidate.model, "/v1/responses", time.Since(started))
				return
			}
			if !startedOutput {
				s.recordModelFailure(candidate.provider, candidate.model, err)
				if index < len(candidates)-1 {
					continue
				}
			}
			end("error", index > 0, candidate.provider, candidate.model, "/v1/responses", time.Since(started))
			if !startedOutput {
				writeError(w, http.StatusBadGateway, "provider_error", publicProviderError(err))
			}
			return
		}
		text, toolCalls, err := collectProviderResponse(r.Context(), runner, &internal)
		status := "ok"
		if err != nil {
			status = "error"
		}
		s.telemetry.recordAttempt(rid, candidate.provider, candidate.model, status, publicProviderError(err), attemptStarted)
		if err != nil {
			s.recordModelFailure(candidate.provider, candidate.model, err)
			if index < len(candidates)-1 {
				continue
			}
			end("error", index > 0, candidate.provider, candidate.model, "/v1/responses", time.Since(started))
			writeError(w, http.StatusBadGateway, "provider_error", publicProviderError(err))
			return
		}
		promptTokens := estimatePromptTokens(&internal)
		completionTokens := estimateTokens(text)
		s.telemetry.recordUsage(rid, promptTokens, completionTokens)
		s.catalog.RecordSuccess(candidate.provider+"/"+candidate.model, time.Now())
		response := buildResponsesResponse(candidate.model, text, toolCalls, promptTokens, completionTokens)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
		end("ok", index > 0, candidate.provider, candidate.model, "/v1/responses", time.Since(started))
		return
	}
	end("error", false, provider, model, "/v1/responses", time.Since(started))
	writeError(w, http.StatusServiceUnavailable, "provider_unavailable", "no routed provider is available")
}

func responsesToOpenAIRequest(request ResponsesRequest) types.OpenAIRequest {
	messages := make([]types.OpenAIMessage, 0, 2)
	if request.Instructions != "" {
		messages = append(messages, types.OpenAIMessage{Role: "system", Content: request.Instructions})
	}
	var inputText string
	if len(request.Input) > 0 {
		if err := json.Unmarshal(request.Input, &inputText); err != nil {
			var items []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(request.Input, &items) == nil {
				for _, item := range items {
					content := responseInputText(item.Content)
					if content != "" {
						role := item.Role
						if role == "" {
							role = "user"
						}
						messages = append(messages, types.OpenAIMessage{Role: role, Content: content})
					}
				}
				return types.OpenAIRequest{Model: request.Model, Messages: messages, Tools: normalizeResponsesTools(request.Tools), ToolChoice: request.ToolChoice, MaxTokens: responseMaxTokens(request.MaxOutputTokens), Temperature: request.Temperature, ReasoningEffort: request.ReasoningEffort}
			}
		} else {
			inputText = strings.TrimSpace(inputText)
		}
	}
	if inputText != "" {
		messages = append(messages, types.OpenAIMessage{Role: "user", Content: inputText})
	}
	return types.OpenAIRequest{Model: request.Model, Messages: messages, Tools: normalizeResponsesTools(request.Tools), ToolChoice: request.ToolChoice, MaxTokens: responseMaxTokens(request.MaxOutputTokens), Temperature: request.Temperature, ReasoningEffort: request.ReasoningEffort}
}

func normalizeResponsesTools(input []ResponsesTool) []types.OpenAITool {
	if len(input) == 0 {
		return nil
	}
	tools := make([]types.OpenAITool, 0, len(input))
	for _, tool := range input {
		if tool.Function != nil {
			tools = append(tools, types.OpenAITool{Type: tool.Type, Function: *tool.Function})
			continue
		}
		tools = append(tools, types.OpenAITool{Type: tool.Type, Function: types.OpenAIToolFunc{
			Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters,
		}})
	}
	return tools
}

func responseInputText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func responseMaxTokens(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

func buildResponsesResponse(model, text string, toolCalls []types.OpenAIToolCall, promptTokens, completionTokens int) ResponsesResponse {
	return buildResponsesResponseWithIDs(model, text, toolCalls, promptTokens, completionTokens, fmt.Sprintf("resp_%d", time.Now().UnixNano()), fmt.Sprintf("msg_%d", time.Now().UnixNano()))
}

func buildResponsesResponseWithIDs(model, text string, toolCalls []types.OpenAIToolCall, promptTokens, completionTokens int, responseID, messageID string) ResponsesResponse {
	output := make([]ResponsesOutput, 0, 1+len(toolCalls))
	if text != "" || len(toolCalls) == 0 {
		output = append(output, ResponsesOutput{ID: messageID, Type: "message", Role: "assistant", Status: "completed", Content: []ResponsesContent{{Type: "output_text", Text: text, Annotations: []any{}}}})
	}
	for _, call := range toolCalls {
		output = append(output, ResponsesOutput{ID: call.ID, Type: "function_call", CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments, Status: "completed"})
	}
	return ResponsesResponse{ID: responseID, Object: "response", CreatedAt: time.Now().Unix(), Status: "completed", Model: model, Output: output, Usage: ResponsesUsage{InputTokens: promptTokens, OutputTokens: completionTokens, TotalTokens: promptTokens + completionTokens}}
}

type responsesStreamState struct {
	responseID  string
	messageID   string
	sequence    int
	messageOpen bool
}

func newResponsesStreamState() responsesStreamState {
	return responsesStreamState{
		responseID: fmt.Sprintf("resp_%d", time.Now().UnixNano()),
		messageID:  fmt.Sprintf("msg_%d", time.Now().UnixNano()),
	}
}

func (state *responsesStreamState) next() int {
	sequence := state.sequence
	state.sequence++
	return sequence
}

func (state *responsesStreamState) writeStart(w http.ResponseWriter, flusher http.Flusher, model string, promptTokens int) error {
	created := buildResponsesResponseWithIDs(model, "", nil, promptTokens, 0, state.responseID, state.messageID)
	created.Status = "in_progress"
	created.Output = nil
	if err := writeResponseEvent(w, flusher, "response.created", map[string]any{
		"type":            "response.created",
		"response":        created,
		"sequence_number": state.next(),
	}); err != nil {
		return err
	}
	if err := writeResponseEvent(w, flusher, "response.in_progress", map[string]any{
		"type":            "response.in_progress",
		"response":        created,
		"sequence_number": state.next(),
	}); err != nil {
		return err
	}
	return nil
}

func (state *responsesStreamState) ensureMessageStarted(w http.ResponseWriter, flusher http.Flusher) error {
	if state.messageOpen {
		return nil
	}
	item := map[string]any{
		"id":      state.messageID,
		"type":    "message",
		"status":  "in_progress",
		"role":    "assistant",
		"content": []any{},
	}
	if err := writeResponseEvent(w, flusher, "response.output_item.added", map[string]any{
		"type":            "response.output_item.added",
		"output_index":    0,
		"item":            item,
		"sequence_number": state.next(),
	}); err != nil {
		return err
	}
	if err := writeResponseEvent(w, flusher, "response.content_part.added", map[string]any{
		"type":            "response.content_part.added",
		"item_id":         state.messageID,
		"output_index":    0,
		"content_index":   0,
		"part":            map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
		"sequence_number": state.next(),
	}); err != nil {
		return err
	}
	state.messageOpen = true
	return nil
}

func (state *responsesStreamState) writeDelta(w http.ResponseWriter, flusher http.Flusher, delta string) error {
	if delta == "" {
		return nil
	}
	if err := state.ensureMessageStarted(w, flusher); err != nil {
		return err
	}
	return writeResponseEvent(w, flusher, "response.output_text.delta", map[string]any{
		"type":            "response.output_text.delta",
		"item_id":         state.messageID,
		"output_index":    0,
		"content_index":   0,
		"delta":           delta,
		"response_id":     state.responseID,
		"logprobs":        []any{},
		"sequence_number": state.next(),
	})
}

func (state *responsesStreamState) writeFinish(w http.ResponseWriter, flusher http.Flusher, model, text string, tools []types.OpenAIToolCall, promptTokens, completionTokens int) error {
	if len(tools) == 0 && !state.messageOpen {
		if err := state.ensureMessageStarted(w, flusher); err != nil {
			return err
		}
	}
	if state.messageOpen {
		if err := writeResponseEvent(w, flusher, "response.output_text.done", map[string]any{
			"type":            "response.output_text.done",
			"item_id":         state.messageID,
			"output_index":    0,
			"content_index":   0,
			"text":            text,
			"logprobs":        []any{},
			"sequence_number": state.next(),
		}); err != nil {
			return err
		}
		if err := writeResponseEvent(w, flusher, "response.content_part.done", map[string]any{
			"type":            "response.content_part.done",
			"item_id":         state.messageID,
			"output_index":    0,
			"content_index":   0,
			"part":            map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
			"sequence_number": state.next(),
		}); err != nil {
			return err
		}
	}
	completed := buildResponsesResponseWithIDs(model, text, tools, promptTokens, completionTokens, state.responseID, state.messageID)
	if state.messageOpen {
		completedItem := ResponsesOutput{ID: state.messageID, Type: "message", Role: "assistant", Status: "completed", Content: []ResponsesContent{{Type: "output_text", Text: text, Annotations: []any{}}}}
		if err := writeResponseEvent(w, flusher, "response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"output_index":    0,
			"item":            completedItem,
			"sequence_number": state.next(),
		}); err != nil {
			return err
		}
	}
	for index, call := range tools {
		outputIndex := index + 1
		item := ResponsesOutput{ID: call.ID, Type: "function_call", CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments, Status: "in_progress"}
		if err := writeResponseEvent(w, flusher, "response.output_item.added", map[string]any{
			"type":            "response.output_item.added",
			"output_index":    outputIndex,
			"item":            item,
			"sequence_number": state.next(),
		}); err != nil {
			return err
		}
		if err := writeResponseEvent(w, flusher, "response.function_call_arguments.delta", map[string]any{
			"type":            "response.function_call_arguments.delta",
			"item_id":         call.ID,
			"output_index":    outputIndex,
			"delta":           call.Function.Arguments,
			"sequence_number": state.next(),
		}); err != nil {
			return err
		}
		if err := writeResponseEvent(w, flusher, "response.function_call_arguments.done", map[string]any{
			"type":            "response.function_call_arguments.done",
			"item_id":         call.ID,
			"output_index":    outputIndex,
			"arguments":       call.Function.Arguments,
			"sequence_number": state.next(),
		}); err != nil {
			return err
		}
		item.Status = "completed"
		if err := writeResponseEvent(w, flusher, "response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"output_index":    outputIndex,
			"item":            item,
			"sequence_number": state.next(),
		}); err != nil {
			return err
		}
	}
	if err := writeResponseEvent(w, flusher, "response.completed", map[string]any{
		"type":            "response.completed",
		"response":        completed,
		"sequence_number": state.next(),
	}); err != nil {
		return err
	}
	_, err := fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
	return err
}

func (state *responsesStreamState) writeFailure(w http.ResponseWriter, flusher http.Flusher, model string, promptTokens, completionTokens int, runErr error) error {
	if err := writeResponseEvent(w, flusher, "error", map[string]any{
		"type":            "error",
		"code":            "provider_error",
		"message":         publicProviderError(runErr),
		"sequence_number": state.next(),
	}); err != nil {
		return err
	}
	failed := buildResponsesResponseWithIDs(model, "", nil, promptTokens, completionTokens, state.responseID, state.messageID)
	failed.Status = "failed"
	failed.Output = nil
	if err := writeResponseEvent(w, flusher, "response.failed", map[string]any{
		"type":            "response.failed",
		"response":        failed,
		"sequence_number": state.next(),
	}); err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) streamResponses(ctx context.Context, w http.ResponseWriter, runner *providers.ProviderRunner, request *types.OpenAIRequest, model string) (bool, int, int, error) {
	events, errorsCh := runner.Invoke(ctx, request)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return false, 0, 0, fmt.Errorf("streaming unsupported")
	}
	first, firstErr := firstProviderEvent(ctx, events, errorsCh)
	if firstErr != nil {
		return false, 0, 0, firstErr
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	promptTokens := estimatePromptTokens(request)
	state := newResponsesStreamState()
	if err := state.writeStart(w, flusher, model, promptTokens); err != nil {
		return true, 0, 0, err
	}
	text := ""
	var toolCalls []types.OpenAIToolCall
	completionTokens := 0
	completed := false
	writeDelta := func(event *providers.StreamEvent) error {
		if event == nil {
			return nil
		}
		if len(event.ToolCalls) > 0 {
			toolCalls = append(toolCalls, event.ToolCalls...)
		}
		if event.Delta != "" {
			text += event.Delta
			completionTokens += estimateTokens(event.Delta)
			return state.writeDelta(w, flusher, event.Delta)
		}
		return nil
	}
	if err := writeDelta(first); err != nil {
		return true, estimatePromptTokens(request), completionTokens, err
	}
	for events != nil || errorsCh != nil {
		select {
		case event, open := <-events:
			if !open {
				events = nil
				if !completed {
					return s.finishResponsesStreamFailure(ctx, w, flusher, state, model, promptTokens, completionTokens, fmt.Errorf("provider stream ended before completion"))
				}
				continue
			}
			if event == nil {
				return s.finishResponsesStreamFailure(ctx, w, flusher, state, model, promptTokens, completionTokens, fmt.Errorf("provider stream returned an empty event"))
			}
			if event.Error != nil {
				return s.finishResponsesStreamFailure(ctx, w, flusher, state, model, promptTokens, completionTokens, event.Error)
			}
			if event.Done {
				completed = true
				events = nil
				continue
			}
			if err := writeDelta(event); err != nil {
				return true, estimatePromptTokens(request), completionTokens, err
			}
		case err, open := <-errorsCh:
			if open && err != nil {
				return s.finishResponsesStreamFailure(ctx, w, flusher, state, model, promptTokens, completionTokens, err)
			}
			errorsCh = nil
		case <-ctx.Done():
			return true, estimatePromptTokens(request), completionTokens, ctx.Err()
		}
	}
	if !completed {
		return s.finishResponsesStreamFailure(ctx, w, flusher, state, model, promptTokens, completionTokens, fmt.Errorf("provider stream ended before completion"))
	}
	if err := state.writeFinish(w, flusher, model, text, toolCalls, promptTokens, completionTokens); err != nil {
		return true, estimatePromptTokens(request), completionTokens, err
	}
	return true, promptTokens, completionTokens, nil
}

func (s *Server) finishResponsesStreamFailure(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, state responsesStreamState, model string, promptTokens, completionTokens int, runErr error) (bool, int, int, error) {
	if ctx.Err() == nil {
		if err := state.writeFailure(w, flusher, model, promptTokens, completionTokens, runErr); err != nil {
			return true, promptTokens, completionTokens, err
		}
	}
	return true, promptTokens, completionTokens, runErr
}

func writeResponseEvent(w http.ResponseWriter, flusher http.Flusher, name string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(w)
	if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", name, payload); err != nil {
		return err
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
