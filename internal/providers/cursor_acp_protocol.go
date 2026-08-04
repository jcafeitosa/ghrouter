package providers

import (
	"encoding/json"
	"fmt"
	"strings"
)

type cursorACPMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *cursorACPError `json:"error"`
}

type cursorACPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Details string `json:"details"`
	} `json:"data"`
}

func (e *cursorACPError) Error() string {
	if e == nil || e.Message == "" {
		return "ACP request failed"
	}
	if details := strings.TrimSpace(e.Data.Details); details != "" {
		return fmt.Sprintf("%s: %s (code %d)", e.Message, details, e.Code)
	}
	return fmt.Sprintf("%s (code %d)", e.Message, e.Code)
}

type cursorACPInitializeResult struct {
	ProtocolVersion int `json:"protocolVersion"`
	AuthMethods     []struct {
		ID string `json:"id"`
	} `json:"authMethods"`
}

type cursorACPSessionResult struct {
	SessionID     string                   `json:"sessionId"`
	ConfigOptions []genericACPConfigOption `json:"configOptions"`
	Models        struct {
		AvailableModels []cursorACPModel `json:"availableModels"`
	} `json:"models"`
}

type cursorACPModel struct {
	ModelID string `json:"modelId"`
	Name    string `json:"name"`
}

type cursorACPUpdateParams struct {
	Update struct {
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"update"`
}

type cursorACPPromptResult struct {
	StopReason string `json:"stopReason"`
}

func cursorACPModelChoice(models []cursorACPModel, requested string) (string, bool) {
	wanted := strings.TrimSpace(stripProviderPrefix(requested))
	if wanted == "" || strings.EqualFold(wanted, "auto") {
		return "", false
	}
	for _, model := range models {
		publicID := cursorACPModelPublicID(model.ModelID)
		if strings.EqualFold(wanted, publicID) || strings.EqualFold(wanted, model.ModelID) || strings.EqualFold(wanted, model.Name) {
			return model.ModelID, true
		}
	}
	return "", false
}

//lint:ignore U1000 retained for ACP model-choice compatibility checks
func cursorACPModelChoiceExists(models []cursorACPModel, requested string) bool {
	_, ok := cursorACPModelChoice(models, requested)
	return ok
}

func cursorACPModelPublicID(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if index := strings.IndexByte(modelID, '['); index >= 0 {
		modelID = modelID[:index]
	}
	if strings.EqualFold(modelID, "default") {
		return "auto"
	}
	return modelID
}

func writeCursorACP(w interface{ Write([]byte) (int, error) }, message map[string]any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

func cursorACPIDMatches(raw json.RawMessage, want int) bool {
	var got int
	return json.Unmarshal(raw, &got) == nil && got == want
}

func cursorACPPermissionResponse(message cursorACPMessage) map[string]any {
	if message.Method != "session/request_permission" {
		return map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message.ID), "error": map[string]any{"code": -32601, "message": "unsupported ACP client request"}}
	}
	var params struct {
		Options []struct {
			OptionID string `json:"optionId"`
		} `json:"options"`
	}
	_ = json.Unmarshal(message.Params, &params)
	if len(params.Options) == 0 || params.Options[0].OptionID == "" {
		return map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message.ID), "result": map[string]any{"outcome": "cancelled"}}
	}
	return map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(message.ID), "result": map[string]any{"outcome": "selected", "optionId": params.Options[0].OptionID}}
}
