package delivery

import (
	"math/rand/v2"
	"time"
)

// Backoff is exponential with jitter: the ceiling doubles per failure up to Cap,
// and the delay is a uniform random value in [Min, ceiling]. The jitter spreads
// retries out so a recovering endpoint isn't hit by a thundering herd, and Min
// avoids near-instant retries.
type Backoff struct {
	Min, Base, Cap time.Duration
}

var DefaultBackoff = Backoff{
	Min:  5 * time.Second,
	Base: 30 * time.Second,
	Cap:  6 * time.Hour,
}

// Delay returns how long to wait after the given number of consecutive failed
// attempts (1 = the first failure). A Retry-After from the receiver wins if longer.
func (b Backoff) Delay(failures int, retryAfter time.Duration) time.Duration {
	return b.delay(failures, retryAfter, rand.Float64)
}

func (b Backoff) delay(failures int, retryAfter time.Duration, rnd func() float64) time.Duration {
	if failures < 1 {
		failures = 1
	}

	ceiling := b.Base
	for i := 1; i < failures && ceiling < b.Cap; i++ {
		ceiling *= 2
	}
	if ceiling > b.Cap {
		ceiling = b.Cap
	}

	d := b.Min
	if ceiling > b.Min {
		d += time.Duration(rnd() * float64(ceiling-b.Min))
	}

	return max(d, retryAfter)
}
