package eventlog

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestNewStore_CreatesSchema(t *testing.T) {
	s := newTestStore(t)
	require.NotNil(t, s)
}

func TestLogEvent_AndQuery(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	err := s.LogEvent(ctx, EventRecord{
		Timestamp:  now,
		Type:       "scale_up",
		AppID:      "app-x",
		ClusterID:  "cluster-1",
		Summary:    "Scaled up app-x on cluster-1",
		DetailJSON: `{"replicas": 3}`,
	})
	require.NoError(t, err)

	events, err := s.QueryEvents(ctx, now.Add(-time.Minute), 100)
	require.NoError(t, err)
	require.Len(t, events, 1)

	assert.Equal(t, "scale_up", events[0].Type)
	assert.Equal(t, "app-x", events[0].AppID)
	assert.Equal(t, "cluster-1", events[0].ClusterID)
	assert.Equal(t, "Scaled up app-x on cluster-1", events[0].Summary)
	assert.Equal(t, `{"replicas": 3}`, events[0].DetailJSON)
	assert.True(t, events[0].ID > 0)
}

func TestLogEvent_MultipleEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	for i := 0; i < 5; i++ {
		err := s.LogEvent(ctx, EventRecord{
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Type:      "cycle_complete",
			Summary:   "Cycle done",
		})
		require.NoError(t, err)
	}

	events, err := s.QueryEvents(ctx, now.Add(-time.Minute), 3)
	require.NoError(t, err)
	assert.Len(t, events, 3, "limit should be respected")
}

func TestQueryEvents_SinceFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, s.LogEvent(ctx, EventRecord{
		Timestamp: now.Add(-time.Hour),
		Type:      "old_event",
		Summary:   "Old",
	}))
	require.NoError(t, s.LogEvent(ctx, EventRecord{
		Timestamp: now,
		Type:      "new_event",
		Summary:   "New",
	}))

	events, err := s.QueryEvents(ctx, now.Add(-time.Minute), 100)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "new_event", events[0].Type)
}

func TestLogStateSnapshot_AndQuery(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	snaps := []ClusterSnapshot{
		{
			Timestamp:     now,
			ClusterID:     "c1",
			TotalGPUs:     16,
			AllocatedGPUs: 8,
			FreeGPUs:      8,
			ReplicaCount:  4,
		},
		{
			Timestamp:     now,
			ClusterID:     "c2",
			TotalGPUs:     32,
			AllocatedGPUs: 16,
			FreeGPUs:      16,
			ReplicaCount:  8,
		},
	}
	require.NoError(t, s.LogStateSnapshot(ctx, snaps))

	results, err := s.QuerySnapshots(ctx, "c1", now.Add(-time.Minute))
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "c1", results[0].ClusterID)
	assert.Equal(t, 16, results[0].TotalGPUs)
	assert.Equal(t, 8, results[0].AllocatedGPUs)
	assert.Equal(t, 8, results[0].FreeGPUs)
	assert.Equal(t, 4, results[0].ReplicaCount)
}

func TestQuerySnapshots_FiltersByCluster(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	snaps := []ClusterSnapshot{
		{Timestamp: now, ClusterID: "c1", TotalGPUs: 8, FreeGPUs: 4},
		{Timestamp: now, ClusterID: "c2", TotalGPUs: 16, FreeGPUs: 8},
	}
	require.NoError(t, s.LogStateSnapshot(ctx, snaps))

	results, err := s.QuerySnapshots(ctx, "c2", now.Add(-time.Minute))
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "c2", results[0].ClusterID)
}

func TestQuerySnapshots_OrderByTimestamp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	for i := 0; i < 3; i++ {
		require.NoError(t, s.LogStateSnapshot(ctx, []ClusterSnapshot{
			{Timestamp: now.Add(time.Duration(i) * time.Minute), ClusterID: "c1", TotalGPUs: i + 1},
		}))
	}

	results, err := s.QuerySnapshots(ctx, "c1", now.Add(-time.Minute))
	require.NoError(t, err)
	require.Len(t, results, 3)
	// Should be ascending by timestamp.
	assert.Equal(t, 1, results[0].TotalGPUs)
	assert.Equal(t, 3, results[2].TotalGPUs)
}

func TestLogEvent_NullableFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	err := s.LogEvent(ctx, EventRecord{
		Timestamp: now,
		Type:      "cycle_complete",
		Summary:   "Cycle done",
		// AppID, ClusterID, DetailJSON intentionally empty
	})
	require.NoError(t, err)

	events, err := s.QueryEvents(ctx, now.Add(-time.Minute), 100)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "", events[0].AppID)
	assert.Equal(t, "", events[0].ClusterID)
	assert.Equal(t, "", events[0].DetailJSON)
}
