package types

import "encoding/json"

const (
	ErrInvalidTrace  = 1001
	ErrAssertionError = 1002
	ErrProviderError  = 2001
	ErrEngineError    = 3001
	ErrTimeout        = 3002
	ErrSessionError   = 3003

	ErrTypeInvalidTrace  = "INVALID_TRACE"
	ErrTypeAssertionError = "ASSERTION_ERROR"
	ErrTypeProviderError  = "PROVIDER_ERROR"
	ErrTypeEngineError    = "ENGINE_ERROR"
	ErrTypeTimeout        = "TIMEOUT"
	ErrTypeSessionError   = "SESSION_ERROR"
)

// NewRPCError constructs an RPCError with the given fields.
func NewRPCError(code int, message string, errorType string, retryable bool, detail string) *RPCError {
	return &RPCError{
		Code:    code,
		Message: message,
		Data: &ErrorData{
			ErrorType: errorType,
			Retryable: retryable,
			Detail:    detail,
		},
	}
}

// NewErrorResponse constructs a JSON-RPC error response.
func NewErrorResponse(id int64, err *RPCError) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   err,
	}
}

// NewSuccessResponse constructs a JSON-RPC success response from a result value.
func NewSuccessResponse(id int64, result any) (*Response, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  raw,
	}, nil
}
