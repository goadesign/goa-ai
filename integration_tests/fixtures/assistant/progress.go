// Package assistantapi reports synthetic work values for independent MCP
// verification. The same unary service method works with or without progress;
// its request context owns delivery and cancellation.
package assistantapi

import (
	"context"
	"time"

	"goa.design/goa-ai/runtime/mcp"
)

// ReportWork reports the three values required by the frozen referee and then
// returns its final result. A canceled request or failed delivery stops work.
func (s *assistantsrvc) ReportWork(ctx context.Context) (string, error) {
	total := float64(100)
	for index, value := range []float64{0, 50, 100} {
		if index > 0 {
			// The referee asks for about 50 milliseconds between synthetic updates.
			// This delay belongs to the fixture, not to a framework rate limit.
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
		if err := mcp.ReportProgress(ctx, value, &total, nil); err != nil {
			return "", err
		}
	}
	return "Work complete", nil
}
