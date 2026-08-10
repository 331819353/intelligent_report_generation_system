package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"intelligent-report-generation-system/internal/askdata"
)

type QueryResult struct {
	Columns []string            `json:"columns"`
	Rows    [][]any             `json:"rows"`
	Plans   []QueryPlanResult   `json:"plans,omitempty"`
	Hash    askdata.ContentHash `json:"hash"`
	Partial bool                `json:"partial"`
}

type QueryPlanResult struct {
	Role    string   `json:"role"`
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

type QueryExecutor interface {
	ExecuteReportQuery(context.Context, QueryRequest) (QueryResult, error)
}

type ComponentResult struct {
	ComponentID askdata.ID     `json:"componentId"`
	State       ComponentState `json:"state"`
	Result      *QueryResult   `json:"result,omitempty"`
	ErrorCode   string         `json:"errorCode,omitempty"`
}

func ExecuteBatch(ctx context.Context, plan ExecutionPlan, executor QueryExecutor, concurrency int) []ComponentResult {
	if concurrency < 1 {
		concurrency = 8
	}
	if concurrency > 16 {
		concurrency = 16
	}
	type group struct {
		request    QueryRequest
		components []askdata.ID
	}
	groups := map[string]*group{}
	results := make([]ComponentResult, 0, len(plan.Components))
	for _, component := range plan.Components {
		if component.Query == nil {
			results = append(results, ComponentResult{ComponentID: component.ComponentID, State: StateReady})
			continue
		}
		hash, err := QueryHash(*component.Query)
		if err != nil {
			results = append(results, ComponentResult{ComponentID: component.ComponentID, State: StateError, ErrorCode: "REPORT_QUERY_HASH_FAILED"})
			continue
		}
		if groups[hash] == nil {
			groups[hash] = &group{request: *component.Query}
		}
		groups[hash].components = append(groups[hash].components, component.ComponentID)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	semaphore := make(chan struct{}, concurrency)
	var wait sync.WaitGroup
	var lock sync.Mutex
	for _, key := range keys {
		item := groups[key]
		wait.Add(1)
		go func() {
			defer wait.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			queryContext := ctx
			cancel := func() {}
			if item.request.Timeout > 0 {
				queryContext, cancel = context.WithTimeout(ctx, item.request.Timeout)
			}
			defer cancel()
			var value QueryResult
			var err error
			if executor == nil {
				err = errors.New("report query executor is unavailable")
			} else {
				value, err = executor.ExecuteReportQuery(queryContext, item.request)
			}
			state, code := queryState(queryContext, value, err)
			lock.Lock()
			defer lock.Unlock()
			for _, componentID := range item.components {
				entry := ComponentResult{ComponentID: componentID, State: state, ErrorCode: code}
				if err == nil {
					copy := value
					entry.Result = &copy
				}
				results = append(results, entry)
			}
		}()
	}
	wait.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].ComponentID < results[j].ComponentID })
	return results
}

func QueryHash(request QueryRequest) (string, error) {
	request.Timeout = 0
	raw, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	return string(askdata.HashBytes(raw)), nil
}

func queryState(ctx context.Context, result QueryResult, err error) (ComponentState, string) {
	if err == nil {
		if len(result.Rows) == 0 {
			return StateEmpty, ""
		}
		if result.Partial {
			return StatePartial, ""
		}
		return StateReady, ""
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return StateTimeout, "REPORT_QUERY_TIMEOUT"
	}
	var coded interface{ Code() string }
	if errors.As(err, &coded) {
		if coded.Code() == "NO_PERMISSION" {
			return StateNoPermission, "NO_PERMISSION"
		}
		if coded.Code() == "REPORT_SEMANTIC_PLAN_STALE" {
			return StateStale, coded.Code()
		}
		return StateError, coded.Code()
	}
	return StateError, fmt.Sprintf("REPORT_QUERY_FAILED")
}
