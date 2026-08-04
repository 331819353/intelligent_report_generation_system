package datasourceai

import "errors"

var (
	ErrInvalidRequest      = errors.New("data source AI request is invalid")
	ErrProviderUnavailable = errors.New("data source AI provider is unavailable")
	ErrInvalidOutput       = errors.New("data source AI output is invalid")
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Draft struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Type         string `json:"type"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Database     string `json:"database"`
	Username     string `json:"username"`
	Visibility   string `json:"visibility"`
	SharingScope string `json:"sharingScope"`
}

type TestFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type TurnRequest struct {
	Instruction      string       `json:"instruction"`
	History          []Message    `json:"history"`
	Draft            Draft        `json:"draft"`
	PasswordProvided bool         `json:"passwordProvided"`
	FileProvided     bool         `json:"fileProvided"`
	TestFailure      *TestFailure `json:"testFailure,omitempty"`
}

type TurnResult struct {
	Reply           string   `json:"reply"`
	Draft           Draft    `json:"draft"`
	MissingFields   []string `json:"missingFields"`
	ReadyToTest     bool     `json:"readyToTest"`
	SuggestedAction string   `json:"suggestedAction"`
	Diagnosis       string   `json:"diagnosis,omitempty"`
	SuggestedChecks []string `json:"suggestedChecks"`
	AutoFixes       []string `json:"autoFixes"`
	AutoRetry       bool     `json:"autoRetry"`
	RequestID       string   `json:"requestId,omitempty"`
}
