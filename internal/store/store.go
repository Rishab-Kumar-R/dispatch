package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Rishab-Kumar-R/dispatch/internal/store/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound            = errors.New("store: not found")
	ErrStale               = errors.New("store: delivery already handled")
	ErrIdempotencyConflict = errors.New("store: idempotency key reused with a different request")
	ErrUnauthenticated     = errors.New("store: invalid api key")
	ErrTenantSuspended     = errors.New("store: tenant suspended")
)

type Store struct {
	pool *pgxpool.Pool
	q    *db.Queries
	now  func() time.Time
}

// New wraps an existing pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{
		pool: pool,
		q:    db.New(pool),
		now:  time.Now,
	}
}

// Open builds a pool that fetches credentials from src on every new connection.
func Open(ctx context.Context, cfg Config, src CredentialsSource) (*Store, error) {
	dsn := fmt.Sprintf("host=%s port=%s dbname=%s sslmode=%s connect_timeout=5",
		cfg.Host, cfg.Port, cfg.Name, cfg.SSLMode)
	pc, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse db config: %w", err)
	}

	pc.MaxConns = cfg.MaxConns
	pc.MinConns = 0
	pc.MaxConnLifetime = 30 * time.Minute
	pc.MaxConnLifetimeJitter = 5 * time.Minute
	pc.MaxConnIdleTime = 5 * time.Minute
	pc.ConnConfig.RuntimeParams["application_name"] = cfg.AppName
	pc.BeforeConnect = func(ctx context.Context, cc *pgx.ConnConfig) error {
		user, pass, err := src.Credentials(ctx)
		if err != nil {
			return err
		}
		cc.User, cc.Password = user, pass
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	return New(pool), nil
}

func (s *Store) Close() {
	s.pool.Close()
}

// Queries gives direct access to the generated queries for simple,
// single-statement reads that need no transaction.
func (s *Store) Queries() *db.Queries {
	return s.q
}

// InTx runs fn in one transaction. It commits if fn returns nil and rolls back otherwise.
func (s *Store) InTx(ctx context.Context, fn func(q *db.Queries) error) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	defer func() {
		if err != nil {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rctx)
		}
	}()

	if err = fn(s.q.WithTx(tx)); err != nil {
		return err
	}

	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

func IsUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}

	return err
}

// newID returns a time-ordered UUIDv7.
func newID() uuid.UUID {
	return uuid.Must(uuid.NewV7())
}
