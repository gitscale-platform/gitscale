package postgres

import (
	"context"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

type repositoryReader struct {
	q querier
}

func (r *repositoryReader) GetByID(_ context.Context, _ uuid.UUID) (*store.Repository, error) {
	return nil, errNotImplemented
}

func (r *repositoryReader) GetBySlug(_ context.Context, _ string) (*store.Repository, error) {
	return nil, errNotImplemented
}

type repositoryWriter struct {
	repositoryReader
}

func (w *repositoryWriter) Insert(_ context.Context, _ store.Repository) error {
	return errNotImplemented
}

func (w *repositoryWriter) UpdatePermissions(_ context.Context, _ uuid.UUID, _ string) error {
	return errNotImplemented
}
