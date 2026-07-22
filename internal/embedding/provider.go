// Package embedding provides a small, provider-neutral boundary for semantic vectors.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

var (
	ErrInvalidRequest  = errors.New("embedding request is invalid")
	ErrUnavailable     = errors.New("embedding provider is unavailable")
	ErrInvalidResponse = errors.New("embedding provider response is invalid")
)

const (
	maxBatchSize     = 32
	maxInputBytes    = 256 << 10
	maxResponseBytes = 32 << 20
)

// Provider embeds bounded plain-text documents. It never receives tenant credentials or source rows.
type Provider interface {
	Configured() bool
	Model() string
	Dimensions() int
	Embed(context.Context, []string) ([][]float32, error)
}

// OpenAICompatibleProvider calls the OpenAI-compatible /embeddings contract.
type OpenAICompatibleProvider struct {
	baseURL    string
	apiKey     string
	model      string
	dimensions int
	http       *http.Client
}

func NewOpenAICompatibleProvider(baseURL, apiKey, model string, dimensions int, client *http.Client) *OpenAICompatibleProvider {
	if client == nil {
		client = http.DefaultClient
	}
	secured := *client
	secured.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &OpenAICompatibleProvider{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), apiKey: strings.TrimSpace(apiKey),
		model: strings.TrimSpace(model), dimensions: dimensions, http: &secured,
	}
}

func (p *OpenAICompatibleProvider) Model() string {
	if p == nil {
		return ""
	}
	return p.model
}

func (p *OpenAICompatibleProvider) Dimensions() int {
	if p == nil {
		return 0
	}
	return p.dimensions
}

func (p *OpenAICompatibleProvider) Configured() bool {
	if p == nil || p.http == nil || p.apiKey == "" || p.model == "" || p.dimensions < 1 || p.dimensions > 16384 {
		return false
	}
	parsed, err := url.Parse(p.baseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	host := strings.TrimSpace(parsed.Hostname())
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type wireRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type wireResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

func (p *OpenAICompatibleProvider) Embed(ctx context.Context, values []string) ([][]float32, error) {
	if !p.Configured() {
		return nil, ErrUnavailable
	}
	if len(values) < 1 || len(values) > maxBatchSize {
		return nil, ErrInvalidRequest
	}
	totalBytes := 0
	input := make([]string, len(values))
	for index, value := range values {
		value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
		totalBytes += len(value)
		if value == "" || totalBytes > maxInputBytes {
			return nil, ErrInvalidRequest
		}
		input[index] = value
	}
	payload, err := json.Marshal(wireRequest{Model: p.model, Input: input})
	if err != nil {
		return nil, ErrInvalidRequest
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, ErrInvalidRequest
	}
	request.Header.Set("Authorization", "Bearer "+p.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := p.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: transport", ErrUnavailable)
	}
	defer response.Body.Close()
	limited := &io.LimitedReader{R: response.Body, N: maxResponseBytes + 1}
	body, readErr := io.ReadAll(limited)
	if readErr != nil {
		return nil, fmt.Errorf("%w: read", ErrUnavailable)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}
	if len(body) > maxResponseBytes {
		return nil, ErrInvalidResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var envelope wireResponse
	if err := decoder.Decode(&envelope); err != nil {
		return nil, ErrInvalidResponse
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || len(envelope.Data) != len(input) {
		return nil, ErrInvalidResponse
	}
	sort.Slice(envelope.Data, func(i, j int) bool { return envelope.Data[i].Index < envelope.Data[j].Index })
	result := make([][]float32, len(input))
	for index, item := range envelope.Data {
		if item.Index != index || len(item.Embedding) != p.dimensions {
			return nil, ErrInvalidResponse
		}
		vector := make([]float32, p.dimensions)
		for position, value := range item.Embedding {
			if math.IsNaN(value) || math.IsInf(value, 0) || value < -1e9 || value > 1e9 {
				return nil, ErrInvalidResponse
			}
			vector[position] = float32(value)
		}
		result[index] = vector
	}
	return result, nil
}
