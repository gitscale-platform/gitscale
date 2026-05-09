package sink_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gitscale-platform/gitscale/plane/git/metering"
	"github.com/gitscale-platform/gitscale/plane/git/sink"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newEvent(op string) metering.MeteringEvent {
	return metering.MeteringEvent{
		EventID:          uuid.NewString(),
		AgentID:          "agent-1",
		RepoID:           uuid.NewString(),
		Operation:        op,
		BytesTransferred: 2048,
		OccurredAt:       time.Now().UTC(),
		EnvelopeVersion:  1,
	}
}

func TestStubSink_RecordAndQuery(t *testing.T) {
	s := sink.NewStubSink()
	evt := newEvent(metering.OpReceivePack)
	require.NoError(t, s.Record(context.Background(), evt))

	all := s.All()
	require.Len(t, all, 1)
	require.Equal(t, evt.EventID, all[0].EventID)
	require.Equal(t, 1, s.Len())
}

func TestStubSink_DuplicateEventIDIsIdempotent(t *testing.T) {
	s := sink.NewStubSink()
	evt := newEvent(metering.OpReceivePack)

	require.NoError(t, s.Record(context.Background(), evt))
	require.NoError(t, s.Record(context.Background(), evt))

	require.Equal(t, 1, s.Len(), "replay of same EventID must be a no-op")
}

func TestStubSink_ConcurrentRecord(t *testing.T) {
	s := sink.NewStubSink()
	const N = 64

	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_ = s.Record(context.Background(), newEvent(metering.OpReceivePack))
		}()
	}
	wg.Wait()

	require.Equal(t, N, s.Len())
}
