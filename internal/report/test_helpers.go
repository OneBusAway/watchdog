package report

import (
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// RecordingTransport captures Sentry events in memory instead of sending them.
type RecordingTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *RecordingTransport) Configure(_ sentry.ClientOptions) {}
func (t *RecordingTransport) Close()                           {}

func (t *RecordingTransport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}

func (t *RecordingTransport) Flush(_ time.Duration) bool { return true }

// Events returns a copy of the captured events.
func (t *RecordingTransport) Events() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]*sentry.Event(nil), t.events...)
}

// CaptureSentry binds a recording transport to the current hub for the duration
// of the test so Sentry reports can be asserted. The previously bound client is
// restored when the test finishes.
func CaptureSentry(t *testing.T) *RecordingTransport {
	t.Helper()
	rec := &RecordingTransport{}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:       "https://public@sentry.example.com/1",
		Transport: rec,
	})
	if err != nil {
		t.Fatalf("sentry.NewClient: %v", err)
	}
	hub := sentry.CurrentHub()
	prev := hub.Client()
	hub.BindClient(client)
	t.Cleanup(func() { hub.BindClient(prev) })
	return rec
}
