package mikrotik

import (
	"errors"
	"fmt"
	"net/http"
)

const errorBodyLimit = 512

// APIError is a non-2xx response from RouterOS.
type APIError struct {
	Operation  string
	URL        string
	StatusCode int
	Message    string
	Detail     string
	Body       string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = e.Body
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return fmt.Sprintf("routeros API error during %s %s: HTTP %d: %s", e.Operation, e.URL, e.StatusCode, msg)
}

// IsNotFound reports whether err is a RouterOS 404 response.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// NetworkError is a transport-level failure.
type NetworkError struct {
	Operation string
	URL       string
	Err       error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("routeros network error during %s %s: %v", e.Operation, e.URL, e.Err)
}

func (e *NetworkError) Unwrap() error { return e.Err }

// DataError is a failure encoding a request or decoding a response.
type DataError struct {
	Operation string
	DataType  string
	Err       error
}

func (e *DataError) Error() string {
	return fmt.Sprintf("routeros data error during %s of %s: %v", e.Operation, e.DataType, e.Err)
}

func (e *DataError) Unwrap() error { return e.Err }

// UnsupportedRecordError is a record RouterOS static DNS cannot represent.
type UnsupportedRecordError struct {
	Name       string
	RecordType string
	Reason     string
}

func (e *UnsupportedRecordError) Error() string {
	return fmt.Sprintf("unsupported routeros DNS record %s %s: %s", e.RecordType, e.Name, e.Reason)
}

type errorResponse struct {
	Error   any    `json:"error"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
}
