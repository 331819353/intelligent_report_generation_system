package dataset

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunBoundedDWDTasksUsesBoundedParallelismAndStableResults(t *testing.T) {
	const (
		taskCount = 7
		limit     = 4
	)
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, taskCount)
	release := make(chan struct{})
	tasks := make([]func(context.Context) (int, error), 0, taskCount)
	for index := range taskCount {
		value := index
		tasks = append(tasks, func(ctx context.Context) (int, error) {
			current := active.Add(1)
			defer active.Add(-1)
			for {
				previous := maximum.Load()
				if current <= previous || maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			started <- struct{}{}
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-release:
				return value, nil
			}
		})
	}
	type outcome struct {
		results []int
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		results, err := runBoundedDWDTasks(
			context.Background(), limit, tasks,
		)
		done <- outcome{results: results, err: err}
	}()
	for range limit {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("bounded DWD tasks did not start concurrently")
		}
	}
	if current := active.Load(); current != limit {
		t.Fatalf("active tasks=%d, want %d", current, limit)
	}
	close(release)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		for index, value := range result.results {
			if value != index {
				t.Fatalf("result[%d]=%d, want stable input order", index, value)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bounded DWD tasks did not finish")
	}
	if maximum.Load() != limit {
		t.Fatalf("maximum concurrency=%d, want %d", maximum.Load(), limit)
	}
}

func TestDWDDimensionStageScopeSurvivesDraftPublication(t *testing.T) {
	datasetID := "00000000-0000-0000-0000-000000000001"
	schemaHash := strings.Repeat("a", 64)
	draftScope, err := dwdDimensionStageScope(map[string]dwdODSAsset{
		"source-version": {
			DatasetID:  datasetID,
			VersionID:  "00000000-0000-0000-0000-000000000002",
			SchemaHash: schemaHash,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	publishedScope, err := dwdDimensionStageScope(map[string]dwdODSAsset{
		"source-version": {
			DatasetID:  datasetID,
			VersionID:  "00000000-0000-0000-0000-000000000003",
			SchemaHash: schemaHash,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if draftScope != publishedScope {
		t.Fatalf(
			"draft publication changed fact checkpoint scope: %s != %s",
			draftScope, publishedScope,
		)
	}
	changedScope, err := dwdDimensionStageScope(map[string]dwdODSAsset{
		"source-version": {
			DatasetID:  datasetID,
			VersionID:  "00000000-0000-0000-0000-000000000004",
			SchemaHash: strings.Repeat("b", 64),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if changedScope == publishedScope {
		t.Fatal("dimension schema change did not invalidate fact checkpoint scope")
	}
}

func TestDimensionOnlyClassificationDoesNotRequireFactModeling(t *testing.T) {
	dimensionOnly := []dwdLLMClassification{{
		DatasetVersionID: "00000000-0000-0000-0000-000000000001",
		Role:             "DIMENSION",
	}}
	if dwdFactProductCount(dimensionOnly) != 0 {
		t.Fatal("dimension-only domain was treated as a fact-modeling workload")
	}
	withFact := append(dimensionOnly, dwdLLMClassification{
		DatasetVersionID: "00000000-0000-0000-0000-000000000002",
		Role:             "FACT",
	})
	if dwdFactProductCount(withFact) != 1 {
		t.Fatal("fact workload count did not preserve the classified fact")
	}
}
