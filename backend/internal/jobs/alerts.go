package jobs

import (
	"context"
	"log/slog"
	"time"

	"phoebe/internal/db"
)

// AlertSweep logs alert.queue_stuck when the oldest due queued job waited longer than
// queueAge, and alert.cost_spike when 24 h AI cost exceeds dailyCostUSD (0 = disabled)
// (backend.md §15). Registered as a sweep, so it fires at most once per SWEEP_INTERVAL.
func AlertSweep(q db.Querier, log *slog.Logger, queueAge time.Duration, dailyCostUSD float64) SweepFunc {
	return func(ctx context.Context) (int, error) {
		alerts := 0
		var ageMS *int64
		if err := q.QueryRow(ctx, `
			SELECT (extract(epoch FROM now() - min(run_after)) * 1000)::bigint
			FROM jobs WHERE status = 'queued' AND run_after <= now()`).Scan(&ageMS); err != nil {
			return alerts, err
		}
		if ageMS != nil && *ageMS > queueAge.Milliseconds() {
			alerts++
			log.ErrorContext(ctx, "alert.queue_stuck", "queue_age_ms", *ageMS)
		}
		if dailyCostUSD > 0 {
			var micros int64
			if err := q.QueryRow(ctx, `
				SELECT COALESCE(sum(cost_usd_micros), 0)::bigint FROM ai_calls
				WHERE created_at > now() - interval '24 hours'`).Scan(&micros); err != nil {
				return alerts, err
			}
			if float64(micros) > dailyCostUSD*1e6 {
				alerts++
				log.ErrorContext(ctx, "alert.cost_spike", "cost_usd", float64(micros)/1e6)
			}
		}
		return alerts, nil
	}
}
