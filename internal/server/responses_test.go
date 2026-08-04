package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ghrouter/internal/types"
)

func TestResponsesRejectsMissingInput(t *testing.T) {
	srv := New(&types.Config{})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"model":"model"}`))
	rec := httptest.NewRecorder()
	srv.handleResponses(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing input, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestResponsesToolsNormalizeTopLevelFunctionSchema(t *testing.T) {
	var request ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"ghrouter/auto","input":"inspect","tools":[{"type":"function","name":"list_files","description":"List files","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}`), &request); err != nil {
		t.Fatalf("decode Responses request: %v", err)
	}
	internal := responsesToOpenAIRequest(request)
	if len(internal.Tools) != 1 {
		t.Fatalf("expected one normalized tool, got %+v", internal.Tools)
	}
	tool := internal.Tools[0].Function
	if tool.Name != "list_files" || tool.Description != "List files" {
		t.Fatalf("unexpected normalized function: %+v", tool)
	}
	if tool.Parameters == nil {
		t.Fatal("expected normalized parameters")
	}
}

func TestResponsesRequestCarriesOutputCapacityIntoRoutingProfile(t *testing.T) {
	request := ResponsesRequest{
		Model:           "ghrouter/auto",
		Input:           json.RawMessage(`"write a concise answer"`),
		MaxOutputTokens: 4096,
		ReasoningEffort: "high",
	}
	internal := responsesToOpenAIRequest(request)
	profile := ProfileRequest(&internal)
	if profile.RequestedOutput != 4096 {
		t.Fatalf("expected requested output capacity to be preserved, got %d", profile.RequestedOutput)
	}
	if profile.ReasoningEffort != "high" {
		t.Fatalf("expected reasoning effort to be preserved, got %q", profile.ReasoningEffort)
	}
}

func TestResponsesToolCallsDoNotEmitDisabledTextPlaceholder(t *testing.T) {
	response := buildResponsesResponseWithIDs("local-model", "", []types.OpenAIToolCall{{
		ID: "call-1", Type: "function", Function: types.OpenAIFunction{Name: "list_files", Arguments: `{"path":"."}`},
	}}, 1, 1, "resp-1", "msg-1")
	if len(response.Output) != 1 || response.Output[0].Type != "function_call" {
		t.Fatalf("expected only a function call output, got %+v", response.Output)
	}
}

func TestResponsesStreamEmitsFunctionCallEvents(t *testing.T) {
	recorder := httptest.NewRecorder()
	state := newResponsesStreamState()
	call := types.OpenAIToolCall{ID: "call-1", Type: "function", Function: types.OpenAIFunction{Name: "list_files", Arguments: `{"path":"."}`}}
	if err := state.writeStart(recorder, recorder, "local-model", 1); err != nil {
		t.Fatalf("write stream start: %v", err)
	}
	if err := state.writeFinish(recorder, recorder, "local-model", "", []types.OpenAIToolCall{call}, 1, 1); err != nil {
		t.Fatalf("write stream finish: %v", err)
	}
	body := recorder.Body.String()
	for _, marker := range []string{"response.function_call_arguments.delta", "response.function_call_arguments.done", `"type":"function_call"`} {
		if !strings.Contains(body, marker) {
			t.Fatalf("stream output missing %q: %s", marker, body)
		}
	}
}
