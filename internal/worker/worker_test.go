package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Rishab-Kumar-R/dispatch/internal/delivery"
	"github.com/Rishab-Kumar-R/dispatch/internal/queue"
	"github.com/Rishab-Kumar-R/dispatch/internal/store"
	"github.com/Rishab-Kumar-R/dispatch/internal/store/db"
)

var fixedNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

type fakeStore struct {
	mu         sync.Mutex
	row        db.GetDeliveryForSendRow
	loadErr    error
	secrets    []db.EndpointSecret
	decision   delivery.Decision
	finishErr  error
	finished   []store.Attempt
	deferredTo []time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		row: db.GetDeliveryForSendRow{
			ID:             uuid.New(),
			EventID:        uuid.New(),
			EndpointID:     uuid.New(),
			Status:         db.DeliveryStatusPending,
			AttemptCount:   2,
			MaxAttempts:    8,
			EndpointUrl:    "https://example.com/hook",
			EndpointStatus: db.EndpointStatusActive,
			EventType:      "invoice.paid",
			Payload:        json.RawMessage(`{"amount":100}`),
			EventCreatedAt: fixedNow,
		},
		secrets: []db.EndpointSecret{{
			ID:         uuid.New(),
			Ciphertext: []byte("ct1"),
		}},
		decision: delivery.Decision{Allow: true},
	}
}

func (f *fakeStore) LoadDelivery(context.Context, uuid.UUID) (db.GetDeliveryForSendRow, error) {
	return f.row, f.loadErr
}
func (f *fakeStore) ActiveSecrets(context.Context, uuid.UUID) ([]db.EndpointSecret, error) {
	return f.secrets, nil
}
func (f *fakeStore) AdmitEndpoint(context.Context, *delivery.Breaker, uuid.UUID) (delivery.Decision, error) {
	return f.decision, nil
}
func (f *fakeStore) FinishAttempt(_ context.Context, _ *delivery.Breaker, a store.Attempt) (store.FinishResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.finishErr != nil {
		return store.FinishResult{}, f.finishErr
	}
	f.finished = append(f.finished, a)
	st := db.DeliveryStatusPending
	if a.Result.Outcome == delivery.OutcomeSuccess {
		st = db.DeliveryStatusSucceeded
	}
	return store.FinishResult{
		Status:        st,
		AttemptNumber: a.ExpectedAttemptCount + 1,
	}, nil
}
func (f *fakeStore) DeferDelivery(_ context.Context, _ uuid.UUID, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deferredTo = append(f.deferredTo, at)
	return nil
}

type fakeDecrypter struct {
	mu    sync.Mutex
	calls int
}

func (d *fakeDecrypter) Decrypt(_ context.Context, _ uuid.UUID, ct []byte) (string, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	return "secret-" + string(ct), nil
}

type fakeSender struct {
	mu    sync.Mutex
	res   delivery.Result
	reqs  []delivery.Request
	panic bool
}

func (s *fakeSender) Send(_ context.Context, r delivery.Request) delivery.Result {
	if s.panic {
		panic("boom")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, r)
	return s.res
}

func newTestWorker(fs *fakeStore, dec *fakeDecrypter, snd *fakeSender) *Worker {
	br, _ := delivery.NewBreaker(delivery.DefaultBreakerConfig)
	return New(Config{
		Store: fs, Secrets: dec, Sender: snd, Breaker: br,
		Backoff: delivery.Backoff{
			Min:  time.Second,
			Base: time.Second,
			Cap:  time.Second,
		},
		Now: func() time.Time {
			return fixedNow
		},
	})
}

func setup(res delivery.Result) (*fakeStore, *fakeDecrypter, *fakeSender, *Worker) {
	fs, dec, snd := newFakeStore(), &fakeDecrypter{}, &fakeSender{res: res}
	return fs, dec, snd, newTestWorker(fs, dec, snd)
}

func TestProcessSuccessSendsSignedEnvelope(t *testing.T) {
	fs, _, snd, w := setup(delivery.Result{
		Outcome:    delivery.OutcomeSuccess,
		StatusCode: 200,
	})

	if err := w.Process(context.Background(), fs.row.ID); err != nil {
		t.Fatal(err)
	}
	if len(snd.reqs) != 1 {
		t.Fatalf("sent %d requests, want 1", len(snd.reqs))
	}
	req := snd.reqs[0]
	if req.URL != "https://example.com/hook" {
		t.Fatalf("url = %s", req.URL)
	}
	if got := req.Header.Get(delivery.HeaderAttempt); got != "3" {
		t.Fatalf("attempt header = %s", got)
	}
	if got := req.Header.Get(delivery.HeaderDeliveryID); got != fs.row.ID.String() {
		t.Fatalf("delivery id header = %s", got)
	}
	if err := delivery.Verify([]string{"secret-ct1"}, req.Header.Get(delivery.HeaderSignature),
		req.Header.Get(delivery.HeaderTimestamp), req.Body, time.Minute, fixedNow); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}

	var env envelope
	if err := json.Unmarshal(req.Body, &env); err != nil {
		t.Fatal(err)
	}
	if env.ID != fs.row.EventID.String() || env.Type != "invoice.paid" || string(env.Data) != `{"amount":100}` {
		t.Fatalf("unexpected envelope: %+v", env)
	}

	if len(fs.finished) != 1 || fs.finished[0].Result.Outcome != delivery.OutcomeSuccess || fs.finished[0].ExpectedAttemptCount != 2 {
		t.Fatalf("finished = %+v", fs.finished)
	}
}

func TestProcessRetrySchedulesBackoff(t *testing.T) {
	fs, _, _, w := setup(delivery.Result{
		Outcome:    delivery.OutcomeRetry,
		StatusCode: 500,
		Err:        errors.New("boom"),
	})
	if err := w.Process(context.Background(), fs.row.ID); err != nil {
		t.Fatal(err)
	}
	if got := fs.finished[0].NextAttemptAt; !got.Equal(fixedNow.Add(time.Second)) {
		t.Fatalf("NextAttemptAt = %v, want now+1s", got)
	}
}

func TestProcessRetryAfterWinsOverBackoff(t *testing.T) {
	fs, _, _, w := setup(delivery.Result{
		Outcome:    delivery.OutcomeRetry,
		StatusCode: 429,
		RetryAfter: 10 * time.Minute,
	})
	if err := w.Process(context.Background(), fs.row.ID); err != nil {
		t.Fatal(err)
	}
	if got := fs.finished[0].NextAttemptAt; !got.Equal(fixedNow.Add(10 * time.Minute)) {
		t.Fatalf("NextAttemptAt = %v, want now+10m", got)
	}
}

func TestProcessPermanentFailure(t *testing.T) {
	fs, _, _, w := setup(delivery.Result{
		Outcome:    delivery.OutcomePermanent,
		StatusCode: 410,
	})
	if err := w.Process(context.Background(), fs.row.ID); err != nil {
		t.Fatal(err)
	}
	if fs.finished[0].Result.Outcome != delivery.OutcomePermanent {
		t.Fatalf("finished = %+v", fs.finished)
	}
}

func TestProcessDefersWhenBreakerDenies(t *testing.T) {
	fs, _, snd, w := setup(delivery.Result{})
	retryAt := fixedNow.Add(time.Hour)
	fs.decision = delivery.Decision{
		Allow:   false,
		RetryAt: retryAt,
	}

	if err := w.Process(context.Background(), fs.row.ID); err != nil {
		t.Fatal(err)
	}
	if len(snd.reqs) != 0 || len(fs.finished) != 0 {
		t.Fatal("a denied delivery must not be sent or consume an attempt")
	}
	if len(fs.deferredTo) != 1 || !fs.deferredTo[0].Equal(retryAt) {
		t.Fatalf("deferredTo = %v", fs.deferredTo)
	}
}

func TestProcessSkipsHandledDeliveries(t *testing.T) {
	for _, st := range []db.DeliveryStatus{
		db.DeliveryStatusSucceeded,
		db.DeliveryStatusDead,
	} {
		fs, _, snd, w := setup(delivery.Result{})
		fs.row.Status = st
		if err := w.Process(context.Background(), fs.row.ID); err != nil || len(snd.reqs) != 0 {
			t.Fatalf("status %s: err=%v sent=%d", st, err, len(snd.reqs))
		}
	}
}

func TestProcessDeliveryGone(t *testing.T) {
	fs, _, snd, w := setup(delivery.Result{})
	fs.loadErr = store.ErrNotFound
	if err := w.Process(context.Background(), uuid.New()); err != nil || len(snd.reqs) != 0 {
		t.Fatalf("err=%v sent=%d", err, len(snd.reqs))
	}
}

func TestProcessDeletedEndpointFailsPermanentlyWithoutSending(t *testing.T) {
	fs, _, snd, w := setup(delivery.Result{})
	deleted := fixedNow
	fs.row.EndpointDeletedAt = &deleted
	if err := w.Process(context.Background(), fs.row.ID); err != nil {
		t.Fatal(err)
	}
	if len(snd.reqs) != 0 || len(fs.finished) != 1 || fs.finished[0].Result.Outcome != delivery.OutcomePermanent {
		t.Fatalf("sent=%d finished=%+v", len(snd.reqs), fs.finished)
	}
}

func TestProcessStaleFinishIsNotAnError(t *testing.T) {
	fs, _, _, w := setup(delivery.Result{
		Outcome:    delivery.OutcomeSuccess,
		StatusCode: 200,
	})
	fs.finishErr = store.ErrStale
	if err := w.Process(context.Background(), fs.row.ID); err != nil {
		t.Fatalf("duplicate SQS delivery should be dropped quietly, got %v", err)
	}
}

func TestProcessInfrastructureErrorsAreReturned(t *testing.T) {
	fs, _, _, w := setup(delivery.Result{})
	fs.loadErr = errors.New("db down")
	if err := w.Process(context.Background(), fs.row.ID); err == nil {
		t.Fatal("expected the error so SQS redelivers the message")
	}

	fs, _, snd, w := setup(delivery.Result{})
	fs.secrets = nil
	if err := w.Process(context.Background(), fs.row.ID); err == nil || len(snd.reqs) != 0 {
		t.Fatalf("missing secret must error without sending: err=%v sent=%d", err, len(snd.reqs))
	}
}

func TestSecretsAreCached(t *testing.T) {
	fs, dec, _, w := setup(delivery.Result{
		Outcome:    delivery.OutcomeSuccess,
		StatusCode: 200,
	})
	for range 3 {
		if err := w.Process(context.Background(), fs.row.ID); err != nil {
			t.Fatal(err)
		}
	}
	if dec.calls != 1 {
		t.Fatalf("KMS decrypt called %d times, want 1", dec.calls)
	}
}

func TestHandleBatchReportsOnlyInfrastructureFailures(t *testing.T) {
	fs, _, _, w := setup(delivery.Result{
		Outcome:    delivery.OutcomeRetry,
		StatusCode: 500,
	})
	good, _ := json.Marshal(queue.Message{
		DeliveryID: fs.row.ID,
	})

	failed := w.HandleBatch(context.Background(), []Record{
		{MessageID: "ok", Body: string(good)},
		{MessageID: "garbage", Body: "not json"},
		{MessageID: "no-id", Body: `{}`},
	})
	if len(failed) != 2 {
		t.Fatalf("failed = %v, want the two malformed messages", failed)
	}
	for _, id := range failed {
		if id == "ok" {
			t.Fatal("a failing customer endpoint must not be reported as an SQS failure")
		}
	}
}

func TestHandleBatchRecoversFromPanics(t *testing.T) {
	fs, _, snd, w := setup(delivery.Result{})
	snd.panic = true
	body, _ := json.Marshal(queue.Message{
		DeliveryID: fs.row.ID,
	})
	failed := w.HandleBatch(context.Background(), []Record{{
		MessageID: "m1",
		Body:      string(body),
	}})
	if len(failed) != 1 || failed[0] != "m1" {
		t.Fatalf("failed = %v", failed)
	}
}
