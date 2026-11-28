package store

import (
	"context"
	"fmt"

	"github.com/Rishab-Kumar-R/dispatch/internal/store/db"
	"github.com/google/uuid"
)

type CreateEndpointInput struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	URL         string
	Description *string
	EventTypes  []string
	Ciphertext  []byte
	KMSKeyID    string
}

// CreateEndpoint inserts the endpoint and its first signing secret atomically.
func (s *Store) CreateEndpoint(ctx context.Context, in CreateEndpointInput) (db.Endpoint, error) {
	var ep db.Endpoint
	err := s.InTx(ctx, func(q *db.Queries) error {
		var err error
		ep, err = q.CreateEndpoint(ctx, db.CreateEndpointParams{
			ID:          in.ID,
			TenantID:    in.TenantID,
			Url:         in.URL,
			Description: in.Description,
			EventTypes:  in.EventTypes,
		})
		if err != nil {
			return fmt.Errorf("insert endpoint: %w", err)
		}

		if _, err = q.CreateEndpointSecret(ctx, db.CreateEndpointSecretParams{
			ID:         newID(),
			EndpointID: in.ID,
			Ciphertext: in.Ciphertext,
			KmsKeyID:   in.KMSKeyID,
		}); err != nil {
			return fmt.Errorf("insert endpoint secret: %w", err)
		}

		return nil
	})

	return ep, err
}

func (s *Store) GetEndpoint(ctx context.Context, tenantID, id uuid.UUID) (db.Endpoint, error) {
	ep, err := s.q.GetEndpoint(ctx, db.GetEndpointParams{
		ID:       id,
		TenantID: tenantID,
	})

	return ep, notFound(err)
}

func (s *Store) ListEndpoints(ctx context.Context, tenantID uuid.UUID, afterID *uuid.UUID, limit int) ([]db.Endpoint, error) {
	return s.q.ListEndpoints(ctx, db.ListEndpointsParams{
		TenantID: tenantID,
		AfterID:  afterID,
		PageSize: int32(limit),
	})
}

func (s *Store) DeleteEndpoint(ctx context.Context, tenantID, id uuid.UUID) error {
	n, err := s.q.SoftDeleteEndpoint(ctx, db.SoftDeleteEndpointParams{
		ID:       id,
		TenantID: tenantID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}

	return nil
}
