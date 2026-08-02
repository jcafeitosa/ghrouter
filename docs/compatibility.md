# Compatibility

The server exposes two compatible surfaces:

- OpenAI Chat Completions
- Anthropic Messages

## Implemented Endpoints

- `POST /v1/chat/completions`
- `POST /v1/messages`
- `GET /v1/models`
- `GET /health`

## Streaming

Streaming uses SSE for both OpenAI-style and Anthropic-style request handling.
