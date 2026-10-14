// Worker heartbeat checks preserve caller settings while delivering cancellation
// for activities that do not configure a heartbeat failure deadline.
package temporal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.temporal.io/sdk/worker"
)

func TestWorkerHeartbeatDefaultsPreserveExplicitSettings(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		configured  worker.Options
		wantDefault time.Duration
		wantMaximum time.Duration
	}{
		{name: "zero settings", wantDefault: 5 * time.Second},
		{
			name:        "explicit default",
			configured:  worker.Options{DefaultHeartbeatThrottleInterval: 9 * time.Second},
			wantDefault: 9 * time.Second,
		},
		{
			name:        "explicit maximum",
			configured:  worker.Options{MaxHeartbeatThrottleInterval: 2 * time.Second},
			wantDefault: 5 * time.Second,
			wantMaximum: 2 * time.Second,
		},
		{
			name: "both explicit",
			configured: worker.Options{
				DefaultHeartbeatThrottleInterval: 9 * time.Second,
				MaxHeartbeatThrottleInterval:     12 * time.Second,
			},
			wantDefault: 9 * time.Second,
			wantMaximum: 12 * time.Second,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			eng := &Engine{workerOpts: test.configured}
			options := eng.workerOptionsForQueue(&workerBundle{})
			assert.Equal(t, test.wantDefault, options.DefaultHeartbeatThrottleInterval)
			assert.Equal(t, test.wantMaximum, options.MaxHeartbeatThrottleInterval)
			assert.Equal(t, test.configured.DefaultHeartbeatThrottleInterval, eng.workerOpts.DefaultHeartbeatThrottleInterval)
			assert.Equal(t, test.configured.MaxHeartbeatThrottleInterval, eng.workerOpts.MaxHeartbeatThrottleInterval)
		})
	}
}
