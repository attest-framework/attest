package types

import "encoding/json"

// Request is a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC error object.
type RPCError struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *ErrorData `json:"data,omitempty"`
}

// ErrorData holds structured error detail.
type ErrorData struct {
	ErrorType string `json:"error_type"`
	Retryable bool   `json:"retryable"`
	Detail    string `json:"detail"`
}

// InitializeParams holds parameters for the initialize method (protocol spec section 2.1).
type InitializeParams struct {
	SDKName              string   `json:"sdk_name"`
	SDKVersion           string   `json:"sdk_version"`
	ProtocolVersion      int      `json:"protocol_version"`
	RequiredCapabilities []string `json:"required_capabilities"`
	PreferredEncoding    string   `json:"preferred_encoding"`
}

// InitializeResult holds the result of the initialize method.
type InitializeResult struct {
	EngineVersion         string   `json:"engine_version"`
	ProtocolVersion       int      `json:"protocol_version"`
	Capabilities          []string `json:"capabilities"`
	Missing               []string `json:"missing"`
	Compatible            bool     `json:"compatible"`
	Encoding              string   `json:"encoding"`
	MaxConcurrentRequests int      `json:"max_concurrent_requests"`
	MaxTraceSizeBytes     int      `json:"max_trace_size_bytes"`
	MaxStepsPerTrace      int      `json:"max_steps_per_trace"`
}

// EvaluateBatchParams holds parameters for the evaluate_batch method.
type EvaluateBatchParams struct {
	Trace      Trace       `json:"trace"`
	Assertions []Assertion `json:"assertions"`
}

// EvaluateBatchResult holds the result of the evaluate_batch method.
type EvaluateBatchResult struct {
	Results         []AssertionResult `json:"results"`
	TotalCost       float64           `json:"total_cost"`
	TotalDurationMS int64             `json:"total_duration_ms"`
}

// ShutdownResult holds the result of the shutdown method.
type ShutdownResult struct {
	SessionsCompleted   int `json:"sessions_completed"`
	AssertionsEvaluated int `json:"assertions_evaluated"`
}

// PluginResult holds the pre-computed result from an SDK-side plugin assertion.
type PluginResult struct {
	Status      string          `json:"status"`
	Score       float64         `json:"score"`
	Explanation string          `json:"explanation"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

// SubmitPluginResultParams holds parameters for the submit_plugin_result method.
type SubmitPluginResultParams struct {
	TraceID     string       `json:"trace_id"`
	PluginName  string       `json:"plugin_name"`
	AssertionID string       `json:"assertion_id"`
	Result      PluginResult `json:"result"`
}

// SubmitPluginResultResponse holds the result of the submit_plugin_result method.
type SubmitPluginResultResponse struct {
	Accepted bool `json:"accepted"`
}

// SimulatePersona describes the personality and LLM parameters for a simulated user.
type SimulatePersona struct {
	Name         string  `json:"name"`
	SystemPrompt string  `json:"system_prompt"`
	Style        string  `json:"style"`
	Temperature  float64 `json:"temperature"`
	MaxTokens    int     `json:"max_tokens,omitempty"`
}

// SimulateFaultConfig describes optional fault injection parameters for simulation.
type SimulateFaultConfig struct {
	ErrorRate         float64 `json:"error_rate"`
	LatencyJitterMS   int     `json:"latency_jitter_ms"`
	ContentCorruption bool    `json:"content_corruption"`
	TimeoutAfterMS    int     `json:"timeout_after_ms"`
}

// ConversationMessage is a single message in a conversation history.
type ConversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// GenerateUserMessageParams holds parameters for the generate_user_message RPC method.
type GenerateUserMessageParams struct {
	Persona             SimulatePersona      `json:"persona"`
	ConversationHistory []ConversationMessage `json:"conversation_history"`
	FaultConfig         *SimulateFaultConfig  `json:"fault_config,omitempty"`
}

// GenerateUserMessageResult holds the result of the generate_user_message RPC method.
type GenerateUserMessageResult struct {
	Message string `json:"message"`
}

// ValidateTraceTreeParams holds parameters for the validate_trace_tree RPC method.
type ValidateTraceTreeParams struct {
	Trace Trace `json:"trace"`
}

// ValidateTraceTreeResult holds the result of the validate_trace_tree RPC method.
type ValidateTraceTreeResult struct {
	Valid              bool     `json:"valid"`
	Errors             []string `json:"errors,omitempty"`
	Depth              int      `json:"depth"`
	AgentCount         int      `json:"agent_count"`
	AgentIDs           []string `json:"agent_ids"`
	AggregateTokens    int      `json:"aggregate_tokens"`
	AggregateCostUSD   float64  `json:"aggregate_cost_usd"`
	AggregateLatencyMS int      `json:"aggregate_latency_ms"`
}
