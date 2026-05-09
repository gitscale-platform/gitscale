// Package dedup provides the Qdrant-backed implementation of
// prnoise.Deduper used in Stage 1 of the PR noise pipeline. The cosine
// similarity threshold is fixed at prnoise.DedupCosineThreshold (0.92,
// quoted from ADR-016 — see prnoise/dedup_threshold.go).
package dedup

import (
	"context"
	"fmt"

	"github.com/gitscale-platform/gitscale/plane/application/prnoise"
	"github.com/google/uuid"
)

// QdrantSearcher is the minimum surface QdrantDeduper needs from a
// Qdrant client. Keeping the dependency this small lets tests inject a
// recording fake without pulling the real driver into the test binary.
type QdrantSearcher interface {
	Search(ctx context.Context, vec []float32, k int, repoFilter *uuid.UUID) ([]QdrantHit, error)
}

// QdrantHit is the per-result projection returned by QdrantSearcher.
type QdrantHit struct {
	PRID  uuid.UUID
	Score float64 // cosine similarity in [-1, 1]
}

// QdrantDeduper implements prnoise.Deduper against a QdrantSearcher.
//
// CrossOrg is read once at construction (the spec calls this out to
// avoid scoring drift mid-evaluation). When false, NearestDuplicate
// passes a non-nil repoFilter to the searcher; when true, the filter
// is dropped.
type QdrantDeduper struct {
	Client   QdrantSearcher
	CrossOrg bool
}

// NewQdrantDeduper returns a Deduper backed by client. crossOrg
// captures the prnoise.cross_org_dedup feature flag at construction.
func NewQdrantDeduper(client QdrantSearcher, crossOrg bool) *QdrantDeduper {
	return &QdrantDeduper{Client: client, CrossOrg: crossOrg}
}

// NearestDuplicate runs a top-1 ANN search and gates on
// prnoise.DedupCosineThreshold. Sub-threshold hits return (nil, nil).
//
// Errors from the searcher are wrapped with %w; partial results are
// never returned.
func (d *QdrantDeduper) NearestDuplicate(ctx context.Context, repoID uuid.UUID, vec []float32) (*prnoise.DuplicateHit, error) {
	var filter *uuid.UUID
	if !d.CrossOrg {
		r := repoID
		filter = &r
	}
	hits, err := d.Client.Search(ctx, vec, 1, filter)
	if err != nil {
		return nil, fmt.Errorf("dedup: qdrant search: %w", err)
	}
	if len(hits) == 0 {
		return nil, nil
	}
	top := hits[0]
	if top.Score < prnoise.DedupCosineThreshold {
		return nil, nil
	}
	return &prnoise.DuplicateHit{PRID: top.PRID, Similarity: top.Score}, nil
}
