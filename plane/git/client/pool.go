// Package client provides a gRPC connection pool to Gitaly file-server nodes.
// Pool entries are keyed by target host:port and reused across requests.
package client

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// GitalyPool maintains one *grpc.ClientConn per Gitaly target address.
// Safe for concurrent use. Close before discarding.
type GitalyPool struct {
	mu     sync.Mutex
	conns  map[string]*grpc.ClientConn
	closed bool
}

// NewGitalyPool returns an empty pool.
func NewGitalyPool() *GitalyPool {
	return &GitalyPool{conns: make(map[string]*grpc.ClientConn)}
}

// Conn returns the existing connection to target or dials a new one.
// target must be a host:port string. Returns an error if the pool is closed
// or if dialing fails.
func (p *GitalyPool) Conn(_ context.Context, target string) (*grpc.ClientConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("gitaly pool: closed")
	}
	if c, ok := p.conns[target]; ok {
		return c, nil
	}
	c, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("gitaly pool: dial %s: %w", target, err)
	}
	p.conns[target] = c
	return c, nil
}

// Close closes all pooled connections. Safe to call multiple times; subsequent
// calls are no-ops. After Close, Conn returns an error.
func (p *GitalyPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
	p.closed = true
}
