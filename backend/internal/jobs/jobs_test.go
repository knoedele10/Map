package jobs

import (
	"context"
	"testing"
	"time"
)

func TestShutdownCancelsAndWaitsForActiveWork(t *testing.T) {
	manager := NewManager(20)
	stopped := make(chan struct{})
	job, err := manager.Start("ingest", func(ctx context.Context, _ func(string), _ func(int, int, string)) (any, error) {
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Shutdown returned before work stopped")
	}
	if job.Snapshot().Status != StatusCancelled {
		t.Fatal(job.Snapshot())
	}
	if _, err := manager.Start("ingest", nil); err == nil {
		t.Fatal("accepted work after shutdown")
	}
}
