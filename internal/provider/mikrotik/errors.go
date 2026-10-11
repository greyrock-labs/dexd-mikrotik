package mikrotik

import "fmt"

const errorBodyLimit = 512

type APIError struct {
	Operation  string
	URL        string
	StatusCode int
	Message    string
	Body       string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("mikrotik API error during %s %s: HTTP %d: %s", e.Operation, e.URL, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("mikrotik API error during %s %s: HTTP %d: %s", e.Operation, e.URL, e.StatusCode, e.Body)
}

type NetworkError struct {
	Operation string
	URL       string
	Err       error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("mikrotik network error during %s %s: %v", e.Operation, e.URL, e.Err)
}

func (e *NetworkError) Unwrap() error {
	return e.Err
}

type DataError struct {
	Operation string
	DataType  string
	Err       error
}

func (e *DataError) Error() string {
	return fmt.Sprintf("mikrotik data error during %s of %s: %v", e.Operation, e.DataType, e.Err)
}

func (e *DataError) Unwrap() error {
	return e.Err
}

type UnsupportedRecordError struct {
	Key        string
	RecordType string
	Reason     string
}

func (e *UnsupportedRecordError) Error() string {
	return fmt.Sprintf("unsupported mikrotik DNS record %s %s: %s", e.RecordType, e.Key, e.Reason)
}

// errorResponse is the RouterOS REST error body, e.g.
// {"error":400,"message":"Bad Request","detail":"failure: entry already exists"}
type errorResponse struct {
	Error   int    `json:"error"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
}
