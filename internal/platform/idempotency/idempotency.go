// Package idempotency provides the shared authenticated write boundary used
// by Ask Data, reports and other governed HTTP bounded contexts.
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	TTL                    = 24 * time.Hour
	MaxKeyBytes            = 256
	MaxResponseBodyBytes   = 2 << 20
	DefaultMaxRequestBytes = 8 << 20
	maxCanonicalJSONDepth  = 64
	maxIdempotencyEndpoint = 512
	MaxExpiredCleanupBatch = 500
)

type State string

const (
	StateAcquired State = "ACQUIRED"
	StateReplay   State = "REPLAY"
	StateInFlight State = "IN_FLIGHT"
	StateReused   State = "REUSED"
)

type Identity struct {
	TenantID string
	ActorID  string
}

func (identity Identity) Valid() bool {
	return canonicalUUID(identity.TenantID) && canonicalUUID(identity.ActorID)
}

type Record struct {
	State          State
	RequestHash    string
	ResponseStatus int
	ResponseBody   []byte
}

type Repository interface {
	Begin(context.Context, Identity, string, string, string, time.Time) (Record, error)
	Complete(context.Context, Identity, string, string, string, int, []byte) error
	Release(context.Context, Identity, string, string, string) error
}

type IdentityResolver func(context.Context) (Identity, error)
type RouteMatcher func(*http.Request) bool
type ErrorWriter func(http.ResponseWriter, int, string, string)

type MiddlewareOptions struct {
	Repository      Repository
	ResolveIdentity IdentityResolver
	Requires        RouteMatcher
	WriteError      ErrorWriter
	MaxRequestBytes int64
	Now             func() time.Time
}

// Middleware runs after authentication and before business mutation. Panics
// and 5xx responses release ownership so a retry can execute; governed 2xx/4xx
// JSON responses are replayed byte-for-byte for 24 hours.
func Middleware(options MiddlewareOptions, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if next == nil || options.Repository == nil || options.ResolveIdentity == nil || options.WriteError == nil {
			writeConfiguredError(options.WriteError, writer, http.StatusInternalServerError,
				"IDEMPOTENCY_SERVICE_FAILED", "idempotency service is unavailable")
			return
		}
		requires := options.Requires
		if requires == nil || !requires(request) {
			next.ServeHTTP(writer, request)
			return
		}
		identity, err := options.ResolveIdentity(request.Context())
		if err != nil || !identity.Valid() {
			options.WriteError(writer, http.StatusUnauthorized,
				"IDEMPOTENCY_IDENTITY_REQUIRED", "authenticated idempotency identity is required")
			return
		}
		key, err := RequireKey(request)
		if err != nil {
			code, message := "IDEMPOTENCY_KEY_INVALID", "Idempotency-Key is invalid"
			if len(request.Header.Values("Idempotency-Key")) == 0 {
				code, message = "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key is required"
			}
			options.WriteError(writer, http.StatusBadRequest, code, message)
			return
		}
		limit := options.MaxRequestBytes
		if limit <= 0 {
			limit = DefaultMaxRequestBytes
		}
		body := request.Body
		if body == nil {
			body = http.NoBody
		}
		raw, err := io.ReadAll(io.LimitReader(body, limit+1))
		if err != nil || int64(len(raw)) > limit {
			options.WriteError(writer, http.StatusBadRequest,
				"IDEMPOTENCY_REQUEST_INVALID", "request body is invalid")
			return
		}
		canonical, requestHash, err := CanonicalRequestBody(raw)
		if err != nil {
			options.WriteError(writer, http.StatusBadRequest,
				"IDEMPOTENCY_REQUEST_INVALID", "request body is invalid")
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(canonical))
		endpoint := request.Method + " " + request.URL.Path
		now := time.Now().UTC()
		if options.Now != nil {
			now = options.Now().UTC()
		}
		record, err := options.Repository.Begin(
			request.Context(), identity, endpoint, key, requestHash, now,
		)
		if err != nil {
			options.WriteError(writer, http.StatusInternalServerError,
				"IDEMPOTENCY_SERVICE_FAILED", "idempotency service is unavailable")
			return
		}
		switch record.State {
		case StateReplay:
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.Header().Set("Idempotency-Replayed", "true")
			writer.WriteHeader(record.ResponseStatus)
			_, _ = writer.Write(record.ResponseBody)
			return
		case StateInFlight:
			options.WriteError(writer, http.StatusConflict,
				"IDEMPOTENCY_IN_FLIGHT", "the first request is still in progress")
			return
		case StateReused:
			options.WriteError(writer, http.StatusConflict,
				"IDEMPOTENCY_KEY_REUSED", "Idempotency-Key was already used with a different request")
			return
		case StateAcquired:
		default:
			options.WriteError(writer, http.StatusInternalServerError,
				"IDEMPOTENCY_SERVICE_FAILED", "idempotency service is unavailable")
			return
		}

		buffered := newBufferedResponseWriter()
		deferredContext := context.WithoutCancel(request.Context())
		defer func() {
			if recovered := recover(); recovered != nil {
				_ = options.Repository.Release(deferredContext, identity, endpoint, key, requestHash)
				panic(recovered)
			}
		}()
		next.ServeHTTP(buffered, request)
		status := buffered.status
		if status == 0 {
			status = http.StatusOK
		}
		if status >= 500 {
			_ = options.Repository.Release(deferredContext, identity, endpoint, key, requestHash)
			writeBufferedResponse(writer, buffered)
			return
		}
		if !json.Valid(buffered.body.Bytes()) {
			_ = options.Repository.Release(deferredContext, identity, endpoint, key, requestHash)
			options.WriteError(writer, http.StatusInternalServerError,
				"IDEMPOTENCY_RESPONSE_INVALID", "idempotent response is not replayable")
			return
		}
		if err := options.Repository.Complete(
			deferredContext, identity, endpoint, key, requestHash, status, buffered.body.Bytes(),
		); err != nil {
			_ = options.Repository.Release(deferredContext, identity, endpoint, key, requestHash)
			options.WriteError(writer, http.StatusInternalServerError,
				"IDEMPOTENCY_SERVICE_FAILED", "idempotency service is unavailable")
			return
		}
		writeBufferedResponse(writer, buffered)
	})
}

func RequireKey(request *http.Request) (string, error) {
	if request == nil {
		return "", errors.New("request is required")
	}
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 {
		return "", errors.New("exactly one Idempotency-Key is required")
	}
	value := strings.TrimSpace(values[0])
	if value == "" || value != values[0] || len(value) > MaxKeyBytes || !utf8.ValidString(value) {
		return "", errors.New("Idempotency-Key is invalid")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", errors.New("Idempotency-Key is invalid")
		}
	}
	return value, nil
}

// CanonicalRequestBody rejects duplicate keys and excessive nesting before
// hashing, then uses encoding/json's stable object-key ordering.
func CanonicalRequestBody(raw []byte) ([]byte, string, error) {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeCanonicalValue(decoder, 0)
	if err != nil {
		return nil, "", err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, "", errors.New("request contains trailing JSON")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	return canonical, Hash(canonical), nil
}

func decodeCanonicalValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > maxCanonicalJSONDepth {
		return nil, errors.New("request JSON is too deeply nested")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		switch token.(type) {
		case nil, bool, string, json.Number:
			return token, nil
		default:
			return nil, errors.New("unsupported JSON token")
		}
	}
	switch delimiter {
	case '{':
		result := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("object key is invalid")
			}
			if _, duplicate := result[key]; duplicate {
				return nil, errors.New("duplicate object key")
			}
			value, err := decodeCanonicalValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			result[key] = value
		}
		if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
			return nil, errors.New("object is not closed")
		}
		return result, nil
	case '[':
		result := []any{}
		for decoder.More() {
			value, err := decodeCanonicalValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		if token, err = decoder.Token(); err != nil || token != json.Delim(']') {
			return nil, errors.New("array is not closed")
		}
		return result, nil
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}

// RequiresGovernedWrite is the common route allowlist. It intentionally does
// not blanket-wrap every POST route.
func RequiresGovernedWrite(request *http.Request) bool {
	if request == nil || request.Method != http.MethodPost && request.Method != http.MethodPut && request.Method != http.MethodDelete {
		return false
	}
	segments := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(segments) < 3 || segments[0] != "api" || segments[1] != "v1" {
		return false
	}
	if len(segments) == 3 {
		return segments[2] == "questions" || segments[2] == "data-requests" ||
			segments[2] == "reports" || segments[2] == "decisions"
	}
	if segments[2] == "work-items" {
		return request.Method == http.MethodPost && len(segments) == 6 && segments[3] != "" && segments[4] != "" && segments[5] == "read"
	}
	if segments[2] == "report-schedules" {
		return (request.Method == http.MethodPost && len(segments) >= 5 && segments[3] != "") ||
			(request.Method == http.MethodDelete && len(segments) == 6 && segments[3] != "" && segments[4] == "subscriptions" && segments[5] != "")
	}
	if segments[2] == "report-deliveries" {
		return request.Method == http.MethodPost && len(segments) == 5 && segments[3] != "" && segments[4] == "read"
	}
	if segments[2] == "users" {
		return request.Method == http.MethodPost && len(segments) == 5 && segments[3] != "" && segments[4] == "deactivation-batches"
	}
	if segments[2] == "runtime-config" {
		if request.Method != http.MethodPost || len(segments) < 4 || segments[3] != "versions" {
			return false
		}
		return len(segments) == 4 ||
			(len(segments) == 6 && segments[4] != "" && (segments[5] == "submit" || segments[5] == "approve" || segments[5] == "apply" || segments[5] == "rollback")) ||
			(len(segments) == 8 && segments[4] != "" && segments[5] == "rollout-nodes" && segments[6] != "" && segments[7] == "restart-ack")
	}
	if segments[2] == "decisions" {
		if request.Method == http.MethodPut {
			return len(segments) == 4 && segments[3] != ""
		}
		return request.Method == http.MethodPost && len(segments) >= 5 && segments[3] != ""
	}
	if len(segments) == 5 && segments[2] == "questions" && segments[3] != "" {
		return segments[4] == "clarifications" || segments[4] == "feedback" ||
			segments[4] == "add-to-report"
	}
	if len(segments) == 5 && segments[2] == "conversations" && segments[3] != "" {
		return request.Method == http.MethodPost && (segments[4] == "release-drift" || segments[4] == "pin" || segments[4] == "archive" || segments[4] == "restore")
	}
	if len(segments) == 7 && segments[2] == "askdata" && segments[3] == "semantic" &&
		segments[4] == "releases" && segments[5] != "" {
		return segments[6] == "activate"
	}
	if segments[2] != "reports" {
		return false
	}
	if request.Method == http.MethodDelete {
		return len(segments) == 6 && segments[3] != "" && segments[4] == "permissions" && segments[5] != ""
	}
	if len(segments) == 5 && segments[3] != "" {
		switch segments[4] {
		case "operations", "undo", "redo", "publish", "rollback", "shares", "exports", "permissions", "archive", "restore", "follow":
			return true
		}
	}
	if len(segments) == 5 && segments[2] == "reports" && segments[3] != "" && segments[4] == "schedules" {
		return request.Method == http.MethodPost
	}
	if len(segments) == 6 && segments[3] != "" && segments[4] == "insights" {
		return segments[5] == "evidence"
	}
	if len(segments) == 7 && segments[3] != "" && segments[4] == "insights" && segments[5] != "" {
		return segments[6] == "edit"
	}
	return len(segments) == 7 && segments[3] != "" && segments[5] != "" &&
		((segments[4] == "shares" && segments[6] == "revoke") ||
			(segments[4] == "exports" && segments[6] == "retry"))
}

func ValidCoordinates(identity Identity, endpoint, key, requestHash string) bool {
	return identity.Valid() && endpoint != "" && len(endpoint) <= maxIdempotencyEndpoint &&
		strings.TrimSpace(endpoint) == endpoint && !hasControl(endpoint) &&
		key != "" && len(key) <= MaxKeyBytes && strings.TrimSpace(key) == key && !hasControl(key) &&
		validHash(requestHash)
}

func Hash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func hasControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

type bufferedResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedResponseWriter() *bufferedResponseWriter {
	return &bufferedResponseWriter{header: make(http.Header)}
}

func (writer *bufferedResponseWriter) Header() http.Header { return writer.header }

func (writer *bufferedResponseWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
	}
}

func (writer *bufferedResponseWriter) Write(value []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	if writer.body.Len()+len(value) > MaxResponseBodyBytes {
		return 0, errors.New("idempotent response exceeds retention limit")
	}
	return writer.body.Write(value)
}

func writeBufferedResponse(destination http.ResponseWriter, source *bufferedResponseWriter) {
	for key, values := range source.header {
		for _, value := range values {
			destination.Header().Add(key, value)
		}
	}
	status := source.status
	if status == 0 {
		status = http.StatusOK
	}
	destination.WriteHeader(status)
	_, _ = destination.Write(source.body.Bytes())
}

func writeConfiguredError(write ErrorWriter, writer http.ResponseWriter, status int, code, message string) {
	if write == nil {
		http.Error(writer, http.StatusText(status), status)
		return
	}
	write(writer, status, code, message)
}
