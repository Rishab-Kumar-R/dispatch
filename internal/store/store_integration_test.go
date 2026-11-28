//go:build integration

package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Rishab-Kumar-R/dispatch/internal/delivery"
	"github.com/Rishab-Kumar-R/dispatch/internal/store/db"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("DISPATCH_TEST_DSN")
	if dsn == "" {
		t.Skip("DISPATCH_TEST_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return New(pool)
}

func seedTenant(t *testing.T, s *Store) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := newID()
	if _, err := s.q.CreateTenant(ctx, db.CreateTenantParams{
		ID:                 id,
		Name:               "it-" + id.String(),
		RateLimitPerMinute: 600},
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id)
	})

	return id
}

func seedEndpoint(t *testing.T, s *Store, tenant uuid.UUID, types ...string) uuid.UUID {
	t.Helper()
	id := newID()
	if types == nil {
		types = []string{}
	}
	if _, err := s.q.CreateEndpoint(context.Background(), db.CreateEndpointParams{
		ID:         id,
		TenantID:   tenant,
		Url:        "https://example.com/hook",
		EventTypes: types,
	}); err != nil {
		t.Fatal(err)
	}

	return id
}

func TestIngestIdempotencyAndFanOut(t *testing.T) {
	ctx, s := context.Background(), testStore(t)
	tenant := seedTenant(t, s)
	seedEndpoint(t, s, tenant)
	seedEndpoint(t, s, tenant, "invoice.paid")
	seedEndpoint(t, s, tenant, "user.created")

	in := IngestInput{
		TenantID:       tenant,
		EventType:      "invoice.paid",
		Payload:        []byte(`{"amount":100}`),
		IdempotencyKey: "k1",
	}
	first, err := s.Ingest(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || len(first.DeliveryIDs) != 2 {
		t.Fatalf("first: duplicate=%v deliveries=%d, want false/2", first.Duplicate, len(first.DeliveryIDs))
	}

	again, err := s.Ingest(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Duplicate || again.Event.ID != first.Event.ID || len(again.DeliveryIDs) != 0 {
		t.Fatalf("retry should return the original event: %+v", again)
	}

	in.Payload = []byte(`{"amount":999}`)
	if _, err := s.Ingest(ctx, in); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("got %v, want ErrIdempotencyConflict", err)
	}
}

func TestOutboxAndFinishAttempt(t *testing.T) {
	ctx, s := context.Background(), testStore(t)
	tenant := seedTenant(t, s)
	endpoint := seedEndpoint(t, s, tenant)
	breaker, _ := delivery.NewBreaker(delivery.DefaultBreakerConfig)

	res, err := s.Ingest(ctx, IngestInput{TenantID: tenant, EventType: "x", Payload: []byte(`{}`)})
	if err != nil || len(res.DeliveryIDs) != 1 {
		t.Fatalf("ingest: %v %+v", err, res)
	}
	deliveryID := res.DeliveryIDs[0]

	var got []uuid.UUID
	collect := func(_ context.Context, b []PendingDelivery) error {
		for _, r := range b {
			if r.TenantID == tenant {
				got = append(got, r.ID)
			}
		}

		return nil
	}
	if _, err := s.PublishPending(ctx, time.Now().Add(time.Minute), 100, collect); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != deliveryID {
		t.Fatalf("outbox published %v, want [%s]", got, deliveryID)
	}

	d, err := s.LoadDelivery(ctx, deliveryID)
	if err != nil {
		t.Fatal(err)
	}

	fail := Attempt{
		DeliveryID:           deliveryID,
		EndpointID:           endpoint,
		ExpectedAttemptCount: d.AttemptCount,
		StartedAt:            time.Now(),
		NextAttemptAt:        time.Now().Add(time.Minute),
		Result: delivery.Result{
			Outcome:    delivery.OutcomeRetry,
			StatusCode: 500,
			Err:        errors.New("boom"),
		},
	}
	fr, err := s.FinishAttempt(ctx, breaker, fail)
	if err != nil || fr.Status != db.DeliveryStatusPending || fr.AttemptNumber != 1 {
		t.Fatalf("finish(fail): %v %+v", err, fr)
	}
	if _, err := s.FinishAttempt(ctx, breaker, fail); !errors.Is(err, ErrStale) {
		t.Fatalf("duplicate finish: got %v, want ErrStale", err)
	}

	ok := Attempt{
		DeliveryID:           deliveryID,
		EndpointID:           endpoint,
		ExpectedAttemptCount: 1,
		StartedAt:            time.Now(),
		Result: delivery.Result{
			Outcome:    delivery.OutcomeSuccess,
			StatusCode: 200,
		},
	}
	sr, err := s.FinishAttempt(ctx, breaker, ok)
	if err != nil || sr.Status != db.DeliveryStatusSucceeded || sr.AttemptNumber != 2 {
		t.Fatalf("finish(ok): %v %+v", err, sr)
	}

	var failures int32
	if err := s.pool.QueryRow(ctx, `SELECT consecutive_failures FROM endpoints WHERE id=$1`, endpoint).Scan(&failures); err != nil || failures != 0 {
		t.Fatalf("breaker not reset: %d %v", failures, err)
	}
}
