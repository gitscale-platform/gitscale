package client_test

import (
	"context"
	"net"
	"testing"

	"github.com/gitscale-platform/gitscale/plane/git/client"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestPool_SameConnReturned(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	pool := client.NewGitalyPool()
	t.Cleanup(pool.Close)

	addr := lis.Addr().String()
	c1, err := pool.Conn(context.Background(), addr)
	require.NoError(t, err)
	c2, err := pool.Conn(context.Background(), addr)
	require.NoError(t, err)
	require.Same(t, c1, c2, "pool must return the same conn for the same addr")
}

func TestPool_DifferentTargetsGetDifferentConns(t *testing.T) {
	lis1, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	lis2, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv1 := grpc.NewServer()
	srv2 := grpc.NewServer()
	go func() { _ = srv1.Serve(lis1) }()
	go func() { _ = srv2.Serve(lis2) }()
	t.Cleanup(srv1.Stop)
	t.Cleanup(srv2.Stop)

	pool := client.NewGitalyPool()
	t.Cleanup(pool.Close)

	c1, err := pool.Conn(context.Background(), lis1.Addr().String())
	require.NoError(t, err)
	c2, err := pool.Conn(context.Background(), lis2.Addr().String())
	require.NoError(t, err)
	require.NotSame(t, c1, c2)
}

func TestPool_CloseIdempotent(t *testing.T) {
	pool := client.NewGitalyPool()
	pool.Close()
	pool.Close()
}

func TestPool_ConnAfterCloseFails(t *testing.T) {
	pool := client.NewGitalyPool()
	pool.Close()
	_, err := pool.Conn(context.Background(), "127.0.0.1:1")
	require.Error(t, err)
}
