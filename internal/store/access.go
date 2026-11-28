package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Rishab-Kumar-R/dispatch/internal/store/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Principal struct {
	APIKeyID           uuid.UUID
	TenantID           uuid.UUID
	RateLimitPerMinute int
}

// Authenticate resolves a hashed API key (auth.HashKey) to its tenant.
func (s *Store) Authenticate(ctx context.Context, keyHash []byte) (Principal, error) {
	row, err := s.q.GetAPIKeyAuth(ctx, keyHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("authenticate: %w", err)
	}
	if row.TenantStatus != db.TenantStatusActive {
		return Principal{}, ErrTenantSuspended
	}

	_ = s.q.TouchAPIKey(ctx, row.ApiKeyID)
	return Principal{
		APIKeyID:           row.ApiKeyID,
		TenantID:           row.TenantID,
		RateLimitPerMinute: int(row.RateLimitPerMinute),
	}, nil
}

// AllowRequest counts this request in the tenant's current one-minute window and
// reports whether it is within the limit. Rejected requests still count.
func (s *Store) AllowRequest(
	ctx context.Context,
	tenantID uuid.UUID,
	limitPerMinute int,
	now time.Time,
) (bool, error) {
	n, err := s.q.IncrementRateLimit(ctx, db.IncrementRateLimitParams{
		TenantID:    tenantID,
		WindowStart: now.UTC().Truncate(time.Minute),
	})
	if err != nil {
		return false, fmt.Errorf("rate limit: %w", err)
	}

	return int(n) <= limitPerMinute, nil
}
