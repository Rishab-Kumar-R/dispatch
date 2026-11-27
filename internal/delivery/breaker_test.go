package delivery

import (
	"testing"
	"time"
)

func newTestBreaker(t *testing.T) *Breaker {
	t.Helper()
	b, err := NewBreaker(BreakerConfig{
		DegradeAfter:  2,
		DisableAfter:  4,
		ProbeInterval: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNewBreakerValidation(t *testing.T) {
	bad := []BreakerConfig{
		{
			DegradeAfter:  0,
			DisableAfter:  5,
			ProbeInterval: time.Minute,
		},
		{
			DegradeAfter:  5,
			DisableAfter:  5,
			ProbeInterval: time.Minute,
		},
		{
			DegradeAfter:  2,
			DisableAfter:  5,
			ProbeInterval: 0,
		},
	}
	for _, cfg := range bad {
		if _, err := NewBreaker(cfg); err == nil {
			t.Errorf("expected error for %+v", cfg)
		}
	}
}

func TestBreakerEscalation(t *testing.T) {
	b := newTestBreaker(t)
	now := time.Unix(1_700_000_000, 0)
	s := State{
		Status: StatusActive,
	}

	want := []Status{
		StatusActive,
		StatusDegraded,
		StatusDegraded,
		StatusDisabled,
	}
	for i, w := range want {
		s = b.Record(s, false, now)
		if s.Status != w || s.ConsecutiveFailures != i+1 {
			t.Fatalf("after failure %d: %+v, want status %s", i+1, s, w)
		}
	}
	if !s.NextProbeAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("NextProbeAt = %v", s.NextProbeAt)
	}
}

func TestBreakerSuccessResets(t *testing.T) {
	b := newTestBreaker(t)
	s := State{Status: StatusDegraded, ConsecutiveFailures: 3}
	s = b.Record(s, true, time.Now())
	if s != (State{Status: StatusActive}) {
		t.Fatalf("got %+v", s)
	}
}

func TestBreakerAdmitActiveAndDegraded(t *testing.T) {
	b := newTestBreaker(t)
	for _, st := range []Status{
		StatusActive,
		StatusDegraded,
	} {
		d, next := b.Admit(State{Status: st}, time.Now())
		if !d.Allow || d.Probe || next.Status != st {
			t.Fatalf("%s: %+v", st, d)
		}
	}
}

func TestBreakerHalfOpenProbe(t *testing.T) {
	b := newTestBreaker(t)
	now := time.Unix(1_700_000_000, 0)
	s := State{
		Status:              StatusDisabled,
		ConsecutiveFailures: 4,
		NextProbeAt:         now.Add(time.Minute),
	}

	d, _ := b.Admit(s, now)
	if d.Allow || !d.RetryAt.Equal(s.NextProbeAt) {
		t.Fatalf("expected denial, got %+v", d)
	}

	at := now.Add(time.Minute)
	d, s2 := b.Admit(s, at)
	if !d.Allow || !d.Probe {
		t.Fatalf("expected probe, got %+v", d)
	}
	d2, _ := b.Admit(s2, at)
	if d2.Allow {
		t.Fatal("second worker must not also probe")
	}

	if got := b.Record(s2, true, at); got.Status != StatusActive {
		t.Fatalf("got %+v", got)
	}
	failed := b.Record(s2, false, at)
	if failed.Status != StatusDisabled || !failed.NextProbeAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("got %+v", failed)
	}
}

func TestBreakerDisabledWithoutProbeTimeAdmitsProbe(t *testing.T) {
	b := newTestBreaker(t)
	d, _ := b.Admit(State{Status: StatusDisabled}, time.Now())
	if !d.Allow || !d.Probe {
		t.Fatalf("got %+v", d)
	}
}
