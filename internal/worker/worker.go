package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Rishab-Kumar-R/dispatch/internal/delivery"
	"github.com/Rishab-Kumar-R/dispatch/internal/queue"
	"github.com/Rishab-Kumar-R/dispatch/internal/store"
	"github.com/Rishab-Kumar-R/dispatch/internal/store/db"
	"github.com/google/uuid"
)

// Store is the slice of *store.Store the worker needs.
type Store interface {
	LoadDelivery(ctx context.Context, id uuid.UUID) (db.GetDeliveryForSendRow, error)
	ActiveSecrets(ctx context.Context, endpointID uuid.UUID) ([]db.EndpointSecret, error)
	AdmitEndpoint(ctx context.Context, b *delivery.Breaker, endpointID uuid.UUID) (delivery.Decision, error)
	FinishAttempt(ctx context.Context, b *delivery.Breaker, a store.Attempt) (store.FinishResult, error)
	DeferDelivery(ctx context.Context, id uuid.UUID, at time.Time) error
}

type Decrypter interface {
	Decrypt(ctx context.Context, endpointID uuid.UUID, ciphertext []byte) (string, error)
}

type Sender interface {
	Send(ctx context.Context, r delivery.Request) delivery.Result
}

type Config struct {
	Store     Store
	Secrets   Decrypter
	Sender    Sender
	Breaker   *delivery.Breaker
	Backoff   delivery.Backoff
	Logger    *slog.Logger
	Now       func() time.Time
	SecretTTL time.Duration
}

type Worker struct {
	Config
	cache *secretCache
}

func New(c Config) *Worker {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.SecretTTL <= 0 {
		c.SecretTTL = 10 * time.Minute
	}

	return &Worker{
		Config: c,
		cache:  newSecretCache(c.SecretTTL, c.Now),
	}
}

// Record is one SQS message, decoupled from the Lambda event types.
type Record struct {
	MessageID, Body string
}

// HandleBatch processes records concurrently and returns the
// IDs of messages SQS should redeliver. Only infrastructure failures are
// returned: a customer endpoint failing is recorded in the DB and retried by
// the relay, not by SQS.
func (w *Worker) HandleBatch(ctx context.Context, recs []Record) []string {
	var (
		mu     sync.Mutex
		failed []string
		wg     sync.WaitGroup
	)
	for _, r := range recs {
		wg.Go(func() {
			if err := w.handleRecord(ctx, r); err != nil {
				mu.Lock()
				failed = append(failed, r.MessageID)
				mu.Unlock()
			}
		})
	}

	wg.Wait()
	return failed
}

func (w *Worker) handleRecord(ctx context.Context, r Record) (err error) {
	defer func() {
		if v := recover(); v != nil {
			w.Logger.ErrorContext(ctx, "panic while processing message", "message_id", r.MessageID, "panic", v)
			err = fmt.Errorf("panic: %v", v)
		}
	}()

	id, err := queue.Decode(r.Body)
	if err != nil {
		w.Logger.ErrorContext(ctx, "undecodable message", "message_id", r.MessageID, "err", err)
		return err
	}
	if err := w.Process(ctx, id); err != nil {
		w.Logger.ErrorContext(ctx, "delivery processing failed", "delivery_id", id, "err", err)
		return err
	}

	return nil
}

type envelope struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}

// Process performs one delivery attempt. A nil return means the SQS message is
// done; an error means infrastructure trouble and SQS should redeliver.
func (w *Worker) Process(ctx context.Context, id uuid.UUID) error {
	log := w.Logger.With("delivery_id", id)

	d, err := w.Store.LoadDelivery(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		log.InfoContext(ctx, "delivery no longer exists; dropping message")
		return nil
	}
	if err != nil {
		return fmt.Errorf("load delivery: %w", err)
	}
	if d.Status != db.DeliveryStatusPending {
		log.InfoContext(ctx, "delivery already handled; dropping message", "status", string(d.Status))
		return nil
	}

	at := store.Attempt{
		DeliveryID:           d.ID,
		EndpointID:           d.EndpointID,
		ExpectedAttemptCount: d.AttemptCount,
	}

	if d.EndpointDeletedAt != nil {
		at.StartedAt = w.Now()
		at.Result = delivery.Result{
			Outcome: delivery.OutcomePermanent,
			Err:     errors.New("endpoint was deleted"),
		}

		return w.finish(ctx, log, at)
	}

	dec, err := w.Store.AdmitEndpoint(ctx, w.Breaker, d.EndpointID)
	if err != nil {
		return fmt.Errorf("admit endpoint: %w", err)
	}
	if !dec.Allow {
		log.InfoContext(ctx, "circuit open; deferring delivery", "retry_at", dec.RetryAt)
		if err := w.Store.DeferDelivery(ctx, d.ID, dec.RetryAt); err != nil {
			return fmt.Errorf("defer delivery: %w", err)
		}

		return nil
	}

	secrets, err := w.secrets(ctx, d.EndpointID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(envelope{
		ID: d.EventID.String(), Type: d.EventType, CreatedAt: d.EventCreatedAt.UTC(), Data: d.Payload,
	})
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}
	attemptNo := int(d.AttemptCount) + 1
	at.StartedAt = w.Now()
	headers, err := delivery.BuildHeaders(delivery.HeaderInput{
		EventID:    d.EventID.String(),
		DeliveryID: d.ID.String(),
		Attempt:    attemptNo,
		Timestamp:  at.StartedAt,
		Secrets:    secrets,
		Body:       body,
	})
	if err != nil {
		return fmt.Errorf("build headers: %w", err)
	}

	res := w.Sender.Send(ctx, delivery.Request{
		URL:    d.EndpointUrl,
		Body:   body,
		Header: headers,
	})
	at.Result = res
	if res.Outcome == delivery.OutcomeRetry {
		at.NextAttemptAt = w.Now().Add(w.Backoff.Delay(attemptNo, res.RetryAfter))
	}

	return w.finish(ctx, log, at)
}

func (w *Worker) finish(ctx context.Context, log *slog.Logger, at store.Attempt) error {
	res, err := w.Store.FinishAttempt(ctx, w.Breaker, at)
	if errors.Is(err, store.ErrStale) {
		log.InfoContext(ctx, "another worker already recorded this attempt")
		return nil
	}
	if err != nil {
		return fmt.Errorf("record attempt: %w", err)
	}

	attrs := []any{
		"outcome", at.Result.Outcome.String(),
		"status_code", at.Result.StatusCode,
		"attempt", res.AttemptNumber,
		"delivery_status", string(res.Status),
		"duration_ms", at.Result.Duration.Milliseconds(),
	}
	if at.Result.Err != nil {
		attrs = append(attrs, "err", at.Result.Err.Error())
	}
	level := slog.LevelInfo
	if res.Status == db.DeliveryStatusDead {
		level = slog.LevelWarn
	}

	log.Log(ctx, level, "delivery attempt finished", attrs...)
	return nil
}

func (w *Worker) secrets(ctx context.Context, endpointID uuid.UUID) ([]string, error) {
	rows, err := w.Store.ActiveSecrets(ctx, endpointID)
	if err != nil {
		return nil, fmt.Errorf("load secrets: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("endpoint %s has no active signing secret", endpointID)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		s, err := w.cache.get(ctx, row.ID, func(ctx context.Context) (string, error) {
			return w.Secrets.Decrypt(ctx, endpointID, row.Ciphertext)
		})
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}

	return out, nil
}

// secretCache keeps decrypted secrets for a few minutes, keyed by the secret
// row's id, so a warm Lambda doesn't call KMS for every delivery.
type secretCache struct {
	mu  sync.Mutex
	ttl time.Duration
	now func() time.Time
	m   map[uuid.UUID]cacheEntry
}

type cacheEntry struct {
	val string
	at  time.Time
}

func newSecretCache(ttl time.Duration, now func() time.Time) *secretCache {
	return &secretCache{
		ttl: ttl,
		now: now,
		m:   make(map[uuid.UUID]cacheEntry),
	}
}

func (c *secretCache) get(ctx context.Context, id uuid.UUID, load func(context.Context) (string, error)) (string, error) {
	c.mu.Lock()
	e, ok := c.m[id]
	c.mu.Unlock()
	if ok && c.now().Sub(e.at) < c.ttl {
		return e.val, nil
	}
	v, err := load(ctx)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	for k, old := range c.m {
		if c.now().Sub(old.at) >= c.ttl {
			delete(c.m, k)
		}
	}
	c.m[id] = cacheEntry{
		val: v,
		at:  c.now(),
	}
	c.mu.Unlock()

	return v, nil
}
