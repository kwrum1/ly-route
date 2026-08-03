package telemetry_test

import (
	"context"
	"testing"
	"time"

	"ly-route/backend/internal/orchestrator/telemetry"
)

func TestCollector_serializes_source_observations(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	source := &blockingSource{
		observation: observation(now, []telemetry.InterfaceCounter{
			{Name: "wan0", LinkUp: true},
			{Name: "lan0", LinkUp: true},
		}),
		entered: make(chan struct{}, 2),
		release: make(chan struct{}, 2),
	}
	collector := telemetry.NewCollector(mustTopology(t, false), source, &fakeClock{now: now})
	done := make(chan struct{}, 2)

	// When
	go func() {
		collector.Collect(context.Background())
		done <- struct{}{}
	}()
	awaitSignal(t, source.entered, "first source observation")
	go func() {
		collector.Collect(context.Background())
		done <- struct{}{}
	}()

	// Then
	select {
	case <-source.entered:
		source.release <- struct{}{}
		source.release <- struct{}{}
		t.Fatal("second source observation entered before first collection completed")
	case <-time.After(50 * time.Millisecond):
	}
	source.release <- struct{}{}
	awaitSignal(t, done, "first collection completion")
	awaitSignal(t, source.entered, "second source observation")
	source.release <- struct{}{}
	awaitSignal(t, done, "second collection completion")
}

type blockingSource struct {
	observation telemetry.Observation
	entered     chan struct{}
	release     chan struct{}
}

func (source *blockingSource) Observe(context.Context) (telemetry.Observation, error) {
	source.entered <- struct{}{}
	<-source.release
	return source.observation, nil
}

func awaitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}
