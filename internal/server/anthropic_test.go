package server

import "testing"

func TestConvertToInternalRequestPreservesMessages(t *testing.T) {
	server := &Server{}
	request := &AnthropicRequest{
		Model:     "cc/model",
		MaxTokens: 128,
		Messages: []AnthropicMessage{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "second"},
		},
	}

	converted := server.convertToInternalRequest(request)
	if len(converted.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(converted.Messages))
	}
	if converted.Messages[0].Content != "first" || converted.Messages[1].Content != "second" {
		t.Fatalf("messages were not preserved: %#v", converted.Messages)
	}
}
