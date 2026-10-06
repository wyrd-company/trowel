package relay

import (
	"testing"
	"time"

	"github.com/wyrd-company/nats-webhook-relay/internal/config"
)

func TestRetryDelay(t *testing.T) {
	w := &Worker{relay: config.SubjectRelay{Consumer: config.Consumer{Backoff: []time.Duration{time.Second, 5 * time.Second}}}}
	cases := map[uint64]time.Duration{0: time.Second, 1: time.Second, 2: 5 * time.Second, 9: 5 * time.Second}
	for attempt, want := range cases {
		if got := w.retryDelay(attempt); got != want {
			t.Errorf("attempt %d: got %s want %s", attempt, got, want)
		}
	}
	if got := (&Worker{}).retryDelay(3); got != 0 {
		t.Errorf("no backoff: got %s", got)
	}
}
