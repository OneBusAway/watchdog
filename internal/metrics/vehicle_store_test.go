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

	go v.ClearRoutine(ctx, time.Millisecond, time.Millisecond)
	
	time.Sleep(50 * time.Millisecond)
	cancel()
	
	if _, ok := v.Get("http://test", "feed1", "v1"); ok {
		t.Fatalf("expected vehicle to be cleared by routine")
	}
}
