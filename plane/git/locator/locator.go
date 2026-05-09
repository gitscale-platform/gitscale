// Package locator resolves a repo_id to its file-server (Gitaly) address.
// CacheLocator wraps cache.GetRepoLocation and falls through to a backing
// RepoLocator (typically MetadataLocator) on a cache miss.
package locator

import (
	"context"
	"errors"
)

// ErrRepoNotFound is returned when no repo with the given ID is known to any
// layer of the locator chain.
var ErrRepoNotFound = errors.New("locator: repo not found")

// FileServerAddr is the resolved routing target for a repository.
// Addr is a host:port string suitable for grpc.NewClient.
type FileServerAddr struct {
	ReplicaSetID string
	HomeRegion   string
	Addr         string
}

// RepoLocator resolves a repo_id (UUID string) to its file-server address.
// Implementations: CacheLocator (L1), MetadataLocator (L2 / source of truth).
type RepoLocator interface {
	Resolve(ctx context.Context, repoID string) (FileServerAddr, error)
}
