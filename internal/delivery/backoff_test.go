package delivery

import (
	"testing"
	"time"
)

func TestBackoffBounds(t *testing.T) {
	b := Backoff{
		Min:  5 * time.Second,
		Base: 30 * time.Second,
		Cap:  time.Hour,
	}
	ceilings := []time.Duration{
		30 * time.Second,
		time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		8 * time.Minute,
		16 * time.Minute,
		32 * time.Minute,
		time.Hour,
		time.Hour,
	}
	for i, ceiling := range ceilings {
		failures := i + 1
		lo := b.delay(failures, 0, func() float64 {
			return 0
		})
		hi := b.delay(failures, 0, func() float64 {
			return 0.999999999
		})
		if lo != b.Min {
			t.Errorf("failures=%d: lo=%v, want %v", failures, lo, b.Min)
		}
		if hi > ceiling || hi < ceiling-time.Millisecond {
			t.Errorf("failures=%d: hi=%v, want ~%v", failures, hi, ceiling)
		}
	}
}

func TestBackoffTreatsZeroFailuresAsOne(t *testing.T) {
	b := DefaultBackoff
	rnd := func() float64 { return 0.5 }
	if b.delay(0, 0, rnd) != b.delay(1, 0, rnd) {
		t.Fatal("failures<1 should behave like 1")
	}
}

func TestBackoffRetryAfterWins(t *testing.T) {
	b := DefaultBackoff
	if got := b.delay(1, 10*time.Minute, func() float64 { return 0.5 }); got != 10*time.Minute {
		t.Fatalf("got %v", got)
	}
}

func TestBackoffNeverBelowMin(t *testing.T) {
	b := DefaultBackoff
	for range 1000 {
		if d := b.Delay(1, 0); d < b.Min {
			t.Fatalf("delay %v below min", d)
		}
	}
}
