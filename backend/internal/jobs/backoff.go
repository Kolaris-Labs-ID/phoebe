package jobs

import (
	"math"
	"math/rand/v2"
	"time"
)

// Backoff: delay = min(cap, base × 3^(attempts−1)) × U(0.8, 1.2), raised to the provider's
// Retry-After when that is ≤ 10 min (backend.md §4.4).
type Backoff struct {
	Base time.Duration
	Cap  time.Duration
}

const maxHonoredRetryAfter = 10 * time.Minute

func (b Backoff) Delay(attempts int, retryAfter time.Duration) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := float64(b.Base) * math.Pow(3, float64(attempts-1))
	if d > float64(b.Cap) {
		d = float64(b.Cap)
	}
	delay := time.Duration(d * (0.8 + 0.4*rand.Float64()))
	if retryAfter > 0 && retryAfter <= maxHonoredRetryAfter && retryAfter > delay {
		delay = retryAfter
	}
	return delay
}

func AIBackoff(base time.Duration) Backoff { return Backoff{Base: base, Cap: 5 * time.Minute} }

func PurgeBackoff() Backoff { return Backoff{Base: time.Minute, Cap: time.Hour} }
