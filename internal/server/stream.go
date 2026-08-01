package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"ghrouter/internal/providers"
	"ghrouter/internal/types"
)

// nonStreamChat collects the CLI output and returns a complete OpenAI response
func (s *Server) nonStreamChat(ctx context.Context, w http.ResponseWriter, runner *providers.ProviderRunner, req *types.OpenAIRequest, model string) {
	events, errs := runner.Invoke(ctx, req)
	text := ""
	done := false
	var runErr error
	for !done {
		select {
		case ev, ok := <-events:
			if !ok {
				done = true
				break
			}
			if ev.Done {
				done = true
				break
			}
			text += ev.Delta
		case err, ok := <-errs:
			if ok {
				runErr = err
			}
			done = true
		case <-time.After(3 * time.Minute):
			runErr = fmt.Errorf("request timed out")
			done = true
		}
	}
	if runErr != nil {
		writeError(w, 502, "provider_error", runErr.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	resp := types.OpenAIResponse{
		ID: fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()), Object: "chat.completion",
		Created: time.Now().Unix(), Model: model,
		Choices: []types.OpenAIChoice{{Index: 0, Message: types.OpenAIMessage{Role: "assistant", Content: text}, FinishReason: "stop"}},
		Usage:   types.OpenAIUsage{PromptTokens: 0, CompletionTokens: estimateTokens(text), TotalTokens: estimateTokens(text)},
	}
	json.NewEncoder(w).Encode(resp)
}

// streamChat emits SSE chunks (OpenAI-compatible) as the CLI streams
func (s *Server) streamChat(ctx context.Context, w http.ResponseWriter, runner *providers.ProviderRunner, req *types.OpenAIRequest, model string) {
	events, errs := runner.Invoke(ctx, req)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	chatID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())

	// role chunk
	s.writeChunk(flusher, chatID, model, types.StreamChoice{Index: 0, Delta: types.StreamDelta{Role: "assistant"}})

	done := false
	for !done {
		select {
		case ev, ok := <-events:
			if !ok {
				done = true
				break
			}
			if ev.Done {
				s.writeChunk(flusher, chatID, model, types.StreamChoice{Index: 0, Delta: types.StreamDelta{}, FinishReason: "stop"})
				done = true
				break
			}
			if ev.Delta != "" {
				s.writeChunk(flusher, chatID, model, types.StreamChoice{Index: 0, Delta: types.StreamDelta{Content: ev.Delta}})
			}
		case err, ok := <-errs:
			if ok {
				// surface error as a delta so the client isn't left hanging
				s.writeChunk(flusher, chatID, model, types.StreamChoice{Index: 0, Delta: types.StreamDelta{Content: fmt.Sprintf("\n[ghrouter error: %v]", err)}})
			}
			done = true
		case <-ctx.Done():
			done = true
		case <-time.After(3 * time.Minute):
			done = true
		}
	}

	// terminate SSE
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (s *Server) writeChunk(flusher http.Flusher, chatID, model string, choice types.StreamChoice) {
	chunk := types.StreamChunk{
		ID: chatID, Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model,
		Choices: []types.StreamChoice{choice},
	}
	b, _ := json.Marshal(chunk)
	fmt.Fprintf(flusher.(http.ResponseWriter), "data: %s\n\n", b)
	flusher.Flush()
}

func estimateTokens(text string) int { return (len(text) + 3) / 4 }
