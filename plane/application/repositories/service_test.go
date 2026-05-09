package repositories_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/application/repositories"
	"github.com/gitscale-platform/gitscale/plane/data/store"
	"github.com/gitscale-platform/gitscale/plane/data/store/stub"
	"github.com/google/uuid"
)

func TestCreateRepository_writesSourceAndOutboxInSameTx(t *testing.T) {
	st := stub.New()
	svc := repositories.NewService(st)

	repo, err := svc.CreateRepository(context.Background(), repositories.CreateInput{
		OrgID: uuid.New(), OwnerID: uuid.New(),
		Slug: "my-repo", Name: "My Repo",
	})
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	rec := st.Recorded()
	if len(rec) != 1 {
		t.Fatalf("outbox records: got %d want 1", len(rec))
	}
	got := rec[0]
	if got.Domain != store.DomainRepositories {
		t.Errorf("domain: %s", got.Domain)
	}
	if got.AggregateID != repo.ID {
		t.Errorf("aggregate_id: %s vs %s", got.AggregateID, repo.ID)
	}
	if got.EventType != repositories.EventRepositoryCreated {
		t.Errorf("event_type: %s", got.EventType)
	}
}

func TestCreateRepository_validatesSlug(t *testing.T) {
	svc := repositories.NewService(stub.New())
	_, err := svc.CreateRepository(context.Background(), repositories.CreateInput{
		OrgID: uuid.New(), OwnerID: uuid.New(), Slug: "BadSlug!", Name: "x",
	})
	if !errors.Is(err, repositories.ErrInvalidSlug) {
		t.Errorf("expected ErrInvalidSlug, got %v", err)
	}
}

func TestCreateRepository_emptyName(t *testing.T) {
	svc := repositories.NewService(stub.New())
	_, err := svc.CreateRepository(context.Background(), repositories.CreateInput{
		OrgID: uuid.New(), OwnerID: uuid.New(), Slug: "ok", Name: "",
	})
	if !errors.Is(err, repositories.ErrEmptyName) {
		t.Errorf("expected ErrEmptyName, got %v", err)
	}
}

func TestCreateRepository_invalidVisibility(t *testing.T) {
	svc := repositories.NewService(stub.New())
	_, err := svc.CreateRepository(context.Background(), repositories.CreateInput{
		OrgID: uuid.New(), OwnerID: uuid.New(), Slug: "ok", Name: "n", Visibility: "weird",
	})
	if !errors.Is(err, repositories.ErrInvalidVisibility) {
		t.Errorf("expected ErrInvalidVisibility, got %v", err)
	}
}

func TestListByOrg_pagesStably(t *testing.T) {
	st := stub.New()
	svc := repositories.NewService(st)
	orgID := uuid.New()

	for i := 0; i < 25; i++ {
		_, err := svc.CreateRepository(context.Background(), repositories.CreateInput{
			OrgID: orgID, OwnerID: uuid.New(),
			Slug: "r-" + uuid.NewString()[:8], Name: "n",
		})
		if err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	seen := map[uuid.UUID]struct{}{}
	cursor := repositories.Cursor{}
	for pages := 0; pages < 100; pages++ {
		rows, next, err := svc.ListByOrg(context.Background(), orgID, cursor, 10)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		for _, r := range rows {
			if _, dup := seen[r.ID]; dup {
				t.Errorf("duplicate id: %s", r.ID)
			}
			seen[r.ID] = struct{}{}
		}
		if next == nil {
			break
		}
		cursor = *next
	}
	if len(seen) != 25 {
		t.Errorf("seen %d, want 25", len(seen))
	}
}
