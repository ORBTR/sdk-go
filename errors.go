package orbtr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// The errors.Is targets of an *APIError, one per refusal class.
var (
	// ErrBadRequest matches a 400: the request is malformed.
	ErrBadRequest = errors.New("orbtr: bad request")
	// ErrAuthentication matches a 401: the key is missing, unknown, revoked, rotated or expired.
	ErrAuthentication = errors.New("orbtr: authentication failed")
	// ErrPaymentRequired matches a 402: the OT balance or a spending bound is short.
	ErrPaymentRequired = errors.New("orbtr: payment required")
	// ErrPermissionDenied matches a 403: the key lacks the scope, the plan lacks the feature, or the request is
	// above a limit.
	ErrPermissionDenied = errors.New("orbtr: permission denied")
	// ErrNotFound matches a 404: the model, flow or batch is unknown or not readable by the key.
	ErrNotFound = errors.New("orbtr: not found")
	// ErrConflict matches a 409: the request conflicts with the conversation or an idempotency key.
	ErrConflict = errors.New("orbtr: conflict")
	// ErrUnprocessableEntity matches a 422: the request is well-formed but cannot be processed.
	ErrUnprocessableEntity = errors.New("orbtr: unprocessable entity")
	// ErrRateLimit matches a 429: a rate, quota or budget limit refused the call.
	ErrRateLimit = errors.New("orbtr: rate limited")
	// ErrInternalServer matches a 5xx: the service failed or is overloaded.
	ErrInternalServer = errors.New("orbtr: server error")
)

// APIError is a response api.orbtr.ai refused. errors.Is matches it against the Err* value of its status.
type APIError struct {
	// StatusCode is the HTTP status.
	StatusCode int
	// Code is the error code the body names, such as invalid_api_key or insufficient_ot.
	Code string
	// Message is the body's readable description.
	Message string
	// Reason is the reason a key_expired_or_revoked refusal names (revoked, rotated or expired).
	Reason string
	// Body is the response body.
	Body []byte
	// Header is the response header.
	Header http.Header
}

func (e *APIError) Error() string {
	if e.Code != "" && e.Message != e.Code {
		return fmt.Sprintf("orbtr: %d %s: %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("orbtr: %d %s", e.StatusCode, e.Message)
}

// Is matches the Err* value of the error's status.
func (e *APIError) Is(target error) bool {
	switch e.StatusCode {
	case 400:
		return target == ErrBadRequest
	case 401:
		return target == ErrAuthentication
	case 402:
		return target == ErrPaymentRequired
	case 403:
		return target == ErrPermissionDenied
	case 404:
		return target == ErrNotFound
	case 409:
		return target == ErrConflict
	case 422:
		return target == ErrUnprocessableEntity
	case 429:
		return target == ErrRateLimit
	}
	return e.StatusCode >= 500 && target == ErrInternalServer
}

// ConnectionError means no response arrived: the connection failed or the call timed out.
type ConnectionError struct {
	// Timeout is true when the call took longer than its timeout.
	Timeout bool
	// Err is the transport's error.
	Err error
}

func (e *ConnectionError) Error() string {
	if e.Timeout {
		return "orbtr: request timed out: " + e.Err.Error()
	}
	return "orbtr: connection error: " + e.Err.Error()
}

func (e *ConnectionError) Unwrap() error { return e.Err }

// StreamError means a stream reported a failure after its response began.
type StreamError struct {
	// Code is the error code the stream named, such as api_error.
	Code string
	// Message is the stream's readable description.
	Message string
	// Body is the event's data.
	Body []byte
}

func (e *StreamError) Error() string {
	if e.Code != "" {
		return "orbtr: stream failed: " + e.Code + ": " + e.Message
	}
	return "orbtr: stream failed: " + e.Message
}

// describeErrorBody reads the code, message and reason from the three error bodies api.orbtr.ai answers:
// the Anthropic shape {"type":"error","error":{"type","message"}}, the OpenAI shape
// {"error":{"message","type","code"}} and the key gateway's {"error":"<code>","reason"?}.
func describeErrorBody(body []byte) (code, message, reason string) {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return "", "", ""
	}
	_ = json.Unmarshal(top["reason"], &reason)
	var s string
	if json.Unmarshal(top["error"], &s) == nil {
		_ = json.Unmarshal(top["message"], &message)
		return s, message, reason
	}
	var obj struct {
		Type    string  `json:"type"`
		Code    *string `json:"code"`
		Message string  `json:"message"`
	}
	if json.Unmarshal(top["error"], &obj) == nil && (obj.Type != "" || obj.Message != "") {
		code = obj.Type
		if obj.Code != nil && *obj.Code != "" {
			code = *obj.Code
		}
		return code, obj.Message, reason
	}
	_ = json.Unmarshal(top["message"], &message)
	return "", message, reason
}

func newAPIError(status int, body []byte, header http.Header) *APIError {
	code, message, reason := describeErrorBody(body)
	if message == "" {
		message = code
	}
	if message == "" {
		message = string(body)
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return &APIError{StatusCode: status, Code: code, Message: message, Reason: reason, Body: body, Header: header}
}
