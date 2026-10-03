package metrics

import (
	"context"
	"testing"
	"time"
)

func TestVehicleLastSeenClear(t *testing.T) {
	v := NewVehicleLastSeen()

	v.Set("http://test", "feed1", "v1", LastSeen{Time: time.Now().Add(-1 * time.Second)})

	v.clear(time.Millisecond)

	if last, ok := v.Get("http://test", "feed1", "v1"); ok {
		t.Fatalf("expected vehicle to be cleared, got %v", last)
	}
}

func TestVehicleLastSeenClearRoutine(t *testing.T) {
	v := NewVehicleLastSeen()
	ctx, cancel := context.WithCancel(context.Background())
	v.Set("http://test", "feed1", "v1", LastSeen{Time: time.Now().Add(-1 * time.Second)})

	done := make(chan struct{})
	go func() {
		defer close(done)
		v.ClearRoutine(ctx, time.Millisecond, time.Millisecond)
	}()
	defer func() {
		cancel()
		<-done
	}()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if _, ok := v.Get("http://test", "feed1", "v1"); !ok {
				return
			}
		case <-deadline.C:
			t.Fatal("expected vehicle to be cleared by routine")
		}
	}
}
