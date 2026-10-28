package jobs

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"quipster/internal/store"
)

type fakeCanceler struct {
	onCancel func()
	mu       sync.Mutex
	calls    int
}

func (f *fakeCanceler) CancelActiveType(jobType string) int {
	if jobType != "probe" {
		return 0
	}
	f.mu.Lock()
	f.calls++
	onCancel := f.onCancel
	f.mu.Unlock()
	if onCancel != nil {
		onCancel()
	}
	return 1
}

func (f *fakeCanceler) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestGateScanCancelsAndWaitsForActiveProbe(t *testing.T) {
	probeCtx, cancelProbe := context.WithCancel(context.Background())
	defer cancelProbe()
	canceler := &fakeCanceler{onCancel: cancelProbe}
	gate := NewGate(canceler)

	probeStarted := make(chan struct{})
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		err := gate.Probe(func(ctx context.Context, _ *store.Job, _ func(float64)) error {
			close(probeStarted)
			<-ctx.Done()
			return ctx.Err()
		})(probeCtx, nil, nil)
		require.ErrorIs(t, err, context.Canceled)
	}()
	select {
	case <-probeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("probe never started")
	}

	scanStarted := make(chan struct{})
	scanGate := make(chan struct{})
	go func() {
		err := gate.Scan(func(context.Context, *store.Job, func(float64)) error {
			close(scanStarted)
			<-scanGate
			return nil
		})(context.Background(), nil, nil)
		require.NoError(t, err)
	}()

	select {
	case <-scanStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("scan did not start after canceling the probe")
	}
	assert.Equal(t, 1, canceler.count())
	select {
	case <-probeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not exit after cancellation")
	}
	close(scanGate)
}

func TestGateProbeWaitsForScan(t *testing.T) {
	gate := NewGate(nil)
	scanStarted := make(chan struct{})
	releaseScan := make(chan struct{})
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		err := gate.Scan(func(context.Context, *store.Job, func(float64)) error {
			close(scanStarted)
			<-releaseScan
			return nil
		})(context.Background(), nil, nil)
		require.NoError(t, err)
	}()
	select {
	case <-scanStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("scan never started")
	}

	probeStarted := make(chan struct{})
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		err := gate.Probe(func(context.Context, *store.Job, func(float64)) error {
			close(probeStarted)
			return nil
		})(context.Background(), nil, nil)
		require.NoError(t, err)
	}()

	select {
	case <-probeStarted:
		t.Fatal("probe ran while the scan held the gate")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseScan)
	select {
	case <-scanDone:
	case <-time.After(2 * time.Second):
		t.Fatal("scan did not finish")
	}
	select {
	case <-probeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not start after the scan finished")
	}
}

func TestGateProbeWaitHonorsContext(t *testing.T) {
	gate := NewGate(nil)
	scanStarted := make(chan struct{})
	releaseScan := make(chan struct{})
	go func() {
		if err := gate.Scan(func(context.Context, *store.Job, func(float64)) error {
			close(scanStarted)
			<-releaseScan
			return nil
		})(context.Background(), nil, nil); err != nil {
			t.Errorf("scan handler: %v", err)
		}
	}()
	select {
	case <-scanStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("scan never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := gate.BeginProbe(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	close(releaseScan)
}

func TestGateEndScanWakesProbe(t *testing.T) {
	gate := NewGate(nil)
	require.NoError(t, gate.BeginScan(context.Background()))
	probeStarted := make(chan struct{})
	go func() {
		if err := gate.BeginProbe(context.Background()); err != nil {
			t.Errorf("BeginProbe: %v", err)
		}
		gate.EndProbe()
		close(probeStarted)
	}()
	select {
	case <-probeStarted:
		t.Fatal("probe acquired the gate before EndScan")
	case <-time.After(10 * time.Millisecond):
	}
	gate.EndScan()
	select {
	case <-probeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("probe was not woken by EndScan")
	}
}
