package datasource

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	maxConnectionTestProgressBytes  = 256 << 10
	maxConnectionTestProgressEvent  = 16 << 10
	maxConnectionTestProgressEvents = 24
)

type connectionTestProgressEvent struct {
	Type          string `json:"type"`
	Stage         string `json:"stage"`
	Status        string `json:"status"`
	Code          string `json:"code"`
	ServerVersion string `json:"serverVersion"`
	LatencyMS     int64  `json:"latencyMs"`
}

var connectionTestStageOrder = map[ConnectionTestStage]int{
	ConnectionTestStageAddress:        0,
	ConnectionTestStagePort:           1,
	ConnectionTestStageDatabase:       2,
	ConnectionTestStageAuthentication: 3,
}

// TestWithProgress consumes the connector's bounded NDJSON control stream. It
// forwards only the four stable stage names; raw targets, credentials and
// database-driver messages never cross this boundary.
func (c *PythonConnector) TestWithProgress(
	ctx context.Context,
	source Source,
	report func(ConnectionTestStage) error,
) (TestResult, error) {
	if c == nil || c.http == nil || c.secrets == nil {
		return TestResult{}, errors.New("staged connector is not configured")
	}
	if report == nil {
		return TestResult{}, errors.New("connection test progress reporter is required")
	}
	input, err := c.connection(ctx, source)
	if err != nil {
		return TestResult{}, err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return TestResult{}, err
	}
	if int64(len(body)) > c.limits.MaxRequestBytes {
		return TestResult{}, ErrConnectorRequestBytesExceeded
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/v1/connections/test/stream",
		bytes.NewReader(body),
	)
	if err != nil {
		return TestResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/x-ndjson")
	request.Header.Set("X-Connector-Token", c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return TestResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return TestResult{}, fmt.Errorf("connector service returned %s", response.Status)
	}

	scanner := bufio.NewScanner(io.LimitReader(response.Body, maxConnectionTestProgressBytes+1))
	scanner.Buffer(make([]byte, 4096), maxConnectionTestProgressEvent)
	lastStage := -1
	passed := map[ConnectionTestStage]bool{}
	events := 0
	for scanner.Scan() {
		events++
		if events > maxConnectionTestProgressEvents {
			return TestResult{}, errors.New("connection test progress contains too many events")
		}
		var event connectionTestProgressEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return TestResult{}, errors.New("connection test progress contains invalid JSON")
		}
		event.Type = strings.ToLower(strings.TrimSpace(event.Type))
		switch event.Type {
		case "stage":
			stage := ConnectionTestStage(strings.ToUpper(strings.TrimSpace(event.Stage)))
			order, valid := connectionTestStageOrder[stage]
			if !valid || order < lastStage {
				return TestResult{}, errors.New("connection test progress stage order is invalid")
			}
			status := strings.ToUpper(strings.TrimSpace(event.Status))
			switch status {
			case "RUNNING":
				lastStage = order
				if err := report(stage); err != nil {
					return TestResult{}, err
				}
			case "PASSED":
				if order != lastStage {
					return TestResult{}, errors.New("connection test progress completion is out of order")
				}
				passed[stage] = true
			default:
				return TestResult{}, errors.New("connection test progress status is invalid")
			}
		case "error":
			code := strings.ToUpper(strings.TrimSpace(event.Code))
			if !safeConnectorErrorCodes[code] {
				code = "CONNECTION_FAILED"
			}
			return TestResult{}, &ConnectorServiceError{Code: code}
		case "complete":
			for stage := range connectionTestStageOrder {
				if !passed[stage] {
					return TestResult{}, errors.New("connection test progress completed before all stages passed")
				}
			}
			if event.LatencyMS < 0 || event.LatencyMS > 900_000 {
				return TestResult{}, errors.New("connection test progress latency is invalid")
			}
			return TestResult{
				ServerVersion: safeServerVersion(event.ServerVersion),
				LatencyMS:     event.LatencyMS,
			}, nil
		default:
			return TestResult{}, errors.New("connection test progress event type is invalid")
		}
	}
	if err := scanner.Err(); err != nil {
		return TestResult{}, err
	}
	return TestResult{}, errors.New("connection test progress ended without a terminal event")
}
