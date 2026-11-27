package delivery

import (
	"errors"
	"time"
)

// Status mirrors the endpoint_status enum in the database
type Status string

const (
	StatusActive   Status = "active"
	StatusDegraded Status = "degraded"
	StatusDisabled Status = "disabled"
)

// State is the breaker state stored on the endpoints row.
type State struct {
	Status              Status
	ConsecutiveFailures int
	NextProbeAt         time.Time
}

type BreakerConfig struct {
	DegradeAfter  int           // consecutive failures before "degraded"
	DisableAfter  int           // consecutive failures before "disabled"
	ProbeInterval time.Duration // how often a disabled endpoint gets one trial request
}

var DefaultBreakerConfig = BreakerConfig{
	DegradeAfter:  5,
	DisableAfter:  20,
	ProbeInterval: time.Hour,
}

// Breaker is a pure state machine. It does no I/O: the store runs it inside a
// transaction holding the endpoint row lock (LockEndpointForUpdate), then
// persists the returned state with SetEndpointBreakerState.
type Breaker struct {
	cfg BreakerConfig
}

func NewBreaker(cfg BreakerConfig) (*Breaker, error) {
	if cfg.DegradeAfter < 1 || cfg.DisableAfter <= cfg.DegradeAfter || cfg.ProbeInterval <= 0 {
		return nil, errors.New("delivery: invalid breaker config")
	}

	return &Breaker{
		cfg: cfg,
	}, nil
}

type Decision struct {
	Allow   bool
	Probe   bool
	RetryAt time.Time
}

// Admit decides whether a request may be sent now. For a disabled endpoint whose
// probe time has arrived it admits exactly one request and pushes NextProbeAt
// out, so concurrent workers see "not due" and are denied.
func (b *Breaker) Admit(s State, now time.Time) (Decision, State) {
	if s.Status != StatusDisabled {
		return Decision{
			Allow: true,
		}, s
	}

	if s.NextProbeAt.IsZero() || !now.Before(s.NextProbeAt) {
		s.NextProbeAt = now.Add(b.cfg.ProbeInterval)
		return Decision{
			Allow: true,
			Probe: true,
		}, s
	}

	return Decision{
		RetryAt: s.NextProbeAt,
	}, s
}

// Record applies the outcome of a delivery attempt. Success always closes the
// breaker (including a successful probe); failures escalate it.
func (b *Breaker) Record(s State, success bool, now time.Time) State {
	if success {
		return State{
			Status: StatusActive,
		}
	}

	s.ConsecutiveFailures++
	switch {
	case s.Status == StatusDisabled:
		s.NextProbeAt = now.Add(b.cfg.ProbeInterval)
	case s.ConsecutiveFailures >= b.cfg.DisableAfter:
		s.Status = StatusDisabled
		s.NextProbeAt = now.Add(b.cfg.ProbeInterval)
	case s.ConsecutiveFailures >= b.cfg.DegradeAfter:
		s.Status = StatusDegraded
	}

	return s
}
