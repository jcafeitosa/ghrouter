package server

import (
	"strings"
	"time"

	"ghrouter/internal/account"
	"ghrouter/internal/catalog"
	"ghrouter/internal/types"
)

type RequestIntent string

const (
	IntentChat      RequestIntent = "chat"
	IntentCode      RequestIntent = "code"
	IntentReasoning RequestIntent = "reasoning"
	IntentVision    RequestIntent = "vision"
)

type CostClass string

const (
	CostClassNormal  CostClass = "normal"
	CostClassEconomy CostClass = "economy"
)

type TaskComplexity string

const (
	ComplexityLow      TaskComplexity = "low"
	ComplexityStandard TaskComplexity = "standard"
	ComplexityHigh     TaskComplexity = "high"
	ComplexityCritical TaskComplexity = "critical"
)

type RequestModality string

const (
	ModalityText  RequestModality = "text"
	ModalityImage RequestModality = "image"
)

type GraphStage string

const (
	GraphAnswer     GraphStage = "answer"
	GraphAnalyze    GraphStage = "analyze"
	GraphCritique   GraphStage = "critique"
	GraphExtract    GraphStage = "extract"
	GraphPlan       GraphStage = "plan"
	GraphImplement  GraphStage = "implement"
	GraphVerify     GraphStage = "verify"
	GraphSynthesize GraphStage = "synthesize"
)

type GraphNode struct {
	ID   GraphStage `json:"id"`
	Role string     `json:"role"`
}

type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type TaskGraph struct {
	Stages     []GraphStage `json:"stages"`
	Nodes      []GraphNode  `json:"nodes"`
	Edges      []GraphEdge  `json:"edges"`
	Parallel   bool         `json:"parallel"`
	NeedsJudge bool         `json:"needs_judge"`
}

type RequestProfile struct {
	Intent           RequestIntent   `json:"intent"`
	Complexity       TaskComplexity  `json:"complexity"`
	Modality         RequestModality `json:"modality"`
	NeedsTools       bool            `json:"needs_tools"`
	NeedsVision      bool            `json:"needs_vision"`
	NeedsLongContext bool            `json:"needs_long_context"`
	ReasoningEffort  string          `json:"reasoning_effort,omitempty"`
	EstimatedTokens  int             `json:"estimated_tokens"`
	RequestedOutput  int             `json:"requested_output_tokens,omitempty"`
	CostClass        CostClass       `json:"cost_class"`
	Graph            TaskGraph       `json:"graph"`
}

func ProfileRequest(req *types.OpenAIRequest) RequestProfile {
	profile := RequestProfile{Intent: IntentChat, Complexity: ComplexityLow, Modality: ModalityText, CostClass: CostClassNormal}
	if req == nil {
		profile.Graph = buildTaskGraph(profile)
		return profile
	}
	profile.NeedsTools = len(req.Tools) > 0 || req.ToolChoice != nil
	profile.ReasoningEffort = strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
	profile.EstimatedTokens = estimatePromptTokens(req)
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		profile.RequestedOutput = *req.MaxTokens
	}
	profile.NeedsLongContext = profile.EstimatedTokens >= 32768
	text, vision := requestProfileText(req.Messages)
	lower := strings.ToLower(text)
	profile.NeedsVision = vision || containsAny(lower, "image", "photo", "screenshot", "diagram", "pdf", "imagem", "foto", "captura", "diagrama")
	if profile.NeedsVision {
		profile.Modality = ModalityImage
	}
	if profile.NeedsTools {
		profile.Intent = IntentCode
	}
	if containsAny(lower, "cheapest", "cheap", "free", "budget", "low cost", "barato", "gratis", "gratuito", "gratuita", "custo", "orcamento") {
		profile.CostClass = CostClassEconomy
	}
	if profile.NeedsVision {
		profile.Intent = IntentVision
	} else if containsAny(lower, "debug", "code", "golang", "go ", "typescript", "python", "refactor", "compile", "test", "codigo", "depurar", "compilar", "implementar", "refatorar", "teste") {
		profile.Intent = IntentCode
	} else if containsAny(lower, "reason", "analyze", "prove", "architecture", "tradeoff", "design", "raciocinio", "analisar", "provar", "arquitetura", "decisao", "planejar", "planejamento") || profile.ReasoningEffort != "" {
		profile.Intent = IntentReasoning
	}
	profile.Complexity = classifyComplexity(profile, lower)
	profile.Graph = buildTaskGraph(profile)
	return profile
}

func classifyComplexity(profile RequestProfile, text string) TaskComplexity {
	if profile.ReasoningEffort == "max" || containsAny(text, "security incident", "production outage", "critical failure", "incidente de seguranca", "indisponibilidade", "falha critica") {
		return ComplexityCritical
	}
	if profile.NeedsLongContext || profile.NeedsTools || profile.ReasoningEffort == "high" || profile.Intent == IntentReasoning || containsAny(text, "architecture", "migration", "refactor", "benchmark", "arquitetura", "migracao", "desempenho") {
		return ComplexityHigh
	}
	if profile.Intent == IntentCode || profile.Intent == IntentVision || profile.EstimatedTokens > 4000 {
		return ComplexityStandard
	}
	return ComplexityLow
}

func buildTaskGraph(profile RequestProfile) TaskGraph {
	graph := TaskGraph{}
	switch profile.Intent {
	case IntentVision:
		graph.Stages = []GraphStage{GraphExtract, GraphAnswer}
		graph.Edges = []GraphEdge{{From: string(GraphExtract), To: string(GraphAnswer)}}
	case IntentCode:
		graph.Stages = []GraphStage{GraphPlan, GraphImplement, GraphVerify}
		graph.Edges = []GraphEdge{{From: string(GraphPlan), To: string(GraphImplement)}, {From: string(GraphImplement), To: string(GraphVerify)}}
	case IntentReasoning:
		graph.Stages = []GraphStage{GraphAnalyze, GraphCritique, GraphSynthesize}
		graph.Edges = []GraphEdge{{From: string(GraphAnalyze), To: string(GraphSynthesize)}, {From: string(GraphCritique), To: string(GraphSynthesize)}}
		graph.Parallel = true
		graph.NeedsJudge = true
	default:
		graph.Stages = []GraphStage{GraphAnswer}
	}
	for _, stage := range graph.Stages {
		graph.Nodes = append(graph.Nodes, GraphNode{ID: stage, Role: string(stage)})
	}
	return graph
}

func prioritizeModelCandidates(candidates []*catalog.ModelEntry, profile RequestProfile, slot catalog.VirtualSlot) []*catalog.ModelEntry {
	if len(candidates) < 2 {
		return candidates
	}
	qualified := candidates
	if slot != catalog.SlotAuto {
		assigned := make([]*catalog.ModelEntry, 0, len(candidates))
		for _, candidate := range qualified {
			if candidate != nil && modelMatchesSlot(candidate, slot) {
				assigned = append(assigned, candidate)
			}
		}
		if len(assigned) > 0 {
			qualified = assigned
		}
	}
	if profile.Intent == IntentReasoning || profile.NeedsLongContext || profile.ReasoningEffort != "" {
		strong := make([]*catalog.ModelEntry, 0, len(candidates))
		for _, candidate := range qualified {
			if candidate == nil {
				continue
			}
			if hasModelCapability(candidate, catalog.CapabilityReasoning) || hasModelCapability(candidate, catalog.CapabilityAutonomous) || candidate.Thinking || (profile.NeedsLongContext && candidate.ContextWindow >= 128000) {
				strong = append(strong, candidate)
			}
		}
		if len(strong) > 0 {
			qualified = strong
		}
	}
	if profile.Intent == IntentCode || profile.NeedsTools {
		coding := make([]*catalog.ModelEntry, 0, len(qualified))
		for _, candidate := range qualified {
			if hasModelCapability(candidate, catalog.CapabilityCode) || candidate.ToolUse {
				coding = append(coding, candidate)
			}
		}
		if len(coding) > 0 {
			qualified = coding
		}
	}
	preferFree := profile.CostClass == CostClassEconomy || (profile.Complexity != ComplexityHigh && profile.Complexity != ComplexityCritical)
	free := make([]*catalog.ModelEntry, 0, len(qualified))
	for _, candidate := range qualified {
		if candidate != nil && candidate.CostTier == catalog.CostFree {
			free = append(free, candidate)
		}
	}
	if preferFree && len(free) > 0 {
		return free
	}
	return qualified
}

func modelMatchesSlot(model *catalog.ModelEntry, slot catalog.VirtualSlot) bool {
	if model == nil {
		return false
	}
	switch slot {
	case catalog.SlotFastCode:
		return hasModelCapability(model, catalog.CapabilityFast) ||
			hasModelCapability(model, catalog.CapabilityCode) ||
			model.ToolUse || model.ContextWindow >= 128000
	case catalog.SlotCheapChat:
		return model.CostTier == catalog.CostFree || model.CostTier == catalog.CostCheap || hasModelCapability(model, catalog.CapabilityCheap)
	case catalog.SlotStrongReason:
		return hasModelCapability(model, catalog.CapabilityReasoning) || hasModelCapability(model, catalog.CapabilityAutonomous) || model.Thinking
	case catalog.SlotLongContext:
		return model.ContextWindow >= 128000 || hasModelCapability(model, catalog.CapabilityLongContext)
	case catalog.SlotVision:
		return model.Vision || hasModelCapability(model, catalog.CapabilityVision)
	case catalog.SlotToolUse:
		return model.ToolUse || hasModelCapability(model, catalog.CapabilityToolUse)
	default:
		return true
	}
}

func hasModelCapability(model *catalog.ModelEntry, capability catalog.CapabilityTag) bool {
	if model == nil {
		return false
	}
	for _, candidate := range model.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

func requestProfileText(messages []types.OpenAIMessage) (string, bool) {
	var text strings.Builder
	vision := false
	for _, message := range messages {
		switch content := message.Content.(type) {
		case string:
			text.WriteString(content)
		case []interface{}:
			for _, part := range content {
				if value, ok := part.(map[string]interface{}); ok {
					if value["type"] == "image_url" || value["type"] == "input_image" {
						vision = true
					}
					if valueText, ok := value["text"].(string); ok {
						text.WriteString(valueText)
					}
				}
			}
		}
	}
	return text.String(), vision
}

func quotaScore(status account.Status) float64 {
	if status.Source == "unsupported" {
		return 0
	}
	if account.Blocked(status) {
		return -100000
	}
	if status.Balance == nil {
		return 0
	}
	if *status.Balance <= 0 && !status.ResetAt.IsZero() && status.ResetAt.After(time.Now()) {
		return -100000
	}
	switch {
	case *status.Balance <= 0:
		return -80
	case *status.Balance < 0.2:
		return -40
	case *status.Balance < 0.5:
		return -15
	case *status.Balance < 0.8:
		return 5
	default:
		return 15
	}
}
