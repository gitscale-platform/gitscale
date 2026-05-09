package repositories

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/google/uuid"
)

// Repository is the application-layer view; aliased to the storage struct
// (same convention as identity.HumanUser).
type Repository = store.Repository

// Service is the repositories domain service.
type Service interface {
	GetRepository(ctx context.Context, id uuid.UUID) (*Repository, error)
	CreateRepository(ctx context.Context, in CreateInput) (*Repository, error)
	ListByOrg(ctx context.Context, orgID uuid.UUID, after Cursor, limit int) ([]Repository, *Cursor, error)
}

// CreateInput is the validated input for CreateRepository.
type CreateInput struct {
	OrgID         uuid.UUID
	OwnerID       uuid.UUID
	Slug          string
	Name          string
	DefaultBranch string
	Visibility    string
}

// Cursor is an opaque keyset pagination cursor over (created_at, id).
type Cursor struct {
	AfterCreatedAt time.Time `json:"after_created_at"`
	AfterID        uuid.UUID `json:"after_id"`
}

// IsZero reports whether c carries no position (use the start of the listing).
func (c Cursor) IsZero() bool {
	return c.AfterID == uuid.Nil && c.AfterCreatedAt.IsZero()
}

// Sentinel errors. Mapped to HTTP codes by plane/application/restapi.
var (
	ErrInvalidSlug         = errors.New("repositories: invalid slug")
	ErrEmptyName           = errors.New("repositories: name is empty")
	ErrInvalidVisibility   = errors.New("repositories: invalid visibility")
	ErrSlugAlreadyExists   = errors.New("repositories: slug already exists in org")
	ErrRepositoryNotFound  = errors.New("repositories: repository not found")
)

// slugPattern mirrors the SQL CHECK constraint `^[a-z0-9][a-z0-9._-]*$`.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func validVisibility(v string) bool {
	switch v {
	case "public", "private", "internal":
		return true
	}
	return false
}

type service struct {
	store store.MetadataStore
	clock func() time.Time
}

// NewService returns a Service backed by ms.
func NewService(ms store.MetadataStore) Service {
	return &service{store: ms, clock: func() time.Time { return time.Now().UTC() }}
}

func (s *service) GetRepository(ctx context.Context, id uuid.UUID) (*Repository, error) {
	return s.store.Repositories().GetByID(ctx, id)
}

func (s *service) CreateRepository(ctx context.Context, in CreateInput) (*Repository, error) {
	if in.Slug == "" || !slugPattern.MatchString(in.Slug) || len(in.Slug) > 100 {
		return nil, ErrInvalidSlug
	}
	if in.Name == "" {
		return nil, ErrEmptyName
	}
	visibility := in.Visibility
	if visibility == "" {
		visibility = "private"
	}
	if !validVisibility(visibility) {
		return nil, ErrInvalidVisibility
	}
	defaultBranch := in.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	now := s.clock()
	repo := Repository{
		ID:            uuid.New(),
		OrgID:         in.OrgID,
		Name:          in.Name,
		Slug:          in.Slug,
		OwnerID:       in.OwnerID,
		DefaultBranch: defaultBranch,
		Visibility:    visibility,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	err := s.store.Transact(ctx, func(tx store.Tx) error {
		if err := tx.Repositories().Insert(ctx, repo); err != nil {
			return err
		}
		return tx.WriteOutbox(ctx, store.DomainRepositories, "repository", repo.ID,
			EventRepositoryCreated, newRepositoryCreatedPayload(repo))
	})
	if err != nil {
		return nil, err
	}
	return &repo, nil
}

func (s *service) ListByOrg(ctx context.Context, orgID uuid.UUID, after Cursor, limit int) ([]Repository, *Cursor, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var afterCreatedAt *time.Time
	var afterID *uuid.UUID
	if !after.IsZero() {
		t := after.AfterCreatedAt
		id := after.AfterID
		afterCreatedAt = &t
		afterID = &id
	}
	rows, err := s.store.Repositories().ListByOrg(ctx, orgID, afterCreatedAt, afterID, limit)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) < limit {
		return rows, nil, nil
	}
	last := rows[len(rows)-1]
	next := Cursor{AfterCreatedAt: last.CreatedAt, AfterID: last.ID}
	return rows, &next, nil
}
