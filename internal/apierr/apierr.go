// Package apierr defines the proxy-generated error type. Every error the
// proxy itself produces carries a stable machine code; relayed upstream errors
// never use this type, so the two are distinguishable by construction.
package apierr

import "fmt"

// Type is the OpenAI-style error `type` for all proxy-generated errors.
const Type = "llmproxy_error"

type ProxyError struct {
	Status  int
	Code    string
	Message string
	Param   string
}

func (e *ProxyError) Error() string { return e.Message }

func New(status int, code, message string) *ProxyError {
	return &ProxyError{Status: status, Code: code, Message: message}
}

func Newf(status int, code, format string, args ...any) *ProxyError {
	return &ProxyError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

func (e *ProxyError) WithParam(param string) *ProxyError {
	e.Param = param
	return e
}
