package logs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCorrelateConnectionsRealData tests correlation with real GCP log data
// from a user query that returned 4 entries (2 incoming + 2 stats)
func TestCorrelateConnectionsRealData(t *testing.T) {
	// Real data from .sbox/logs.jsonl - 2 requests with their stats
	entries := []LogEntry{
		// Request 1: incoming
		{
			Message:          "incoming Substreams Blocks request",
			TraceID:          "b8b020a93c7c48502103ede171a7844c",
			UserID:           "0bevuabef54becd7741bf",
			IPAddress:        "198.58.208.52",
			OutputModule:     "map_clocks",
			OutputModuleHash: "7685a04836b6bac7ec654589bf1fe79ec3decbe1",
			StartBlock:       -1,
			StopBlock:        0,
			ProductionMode:   false,
			Timestamp:        "2026-02-12T18:01:35.352273637Z",
			Namespace:        "eth-bsc-mainnet",
			ClusterName:      "saas-us-central1",
			PodName:          "substreams-rpcv2-tier1-78c6987dc8-zlccb",
		},
		// Request 1: stats
		{
			Message:              "substreams request stats",
			Tier:                 "tier1",
			TraceID:              "b8b020a93c7c48502103ede171a7844c",
			UserID:               "0bevuabef54becd7741bf",
			OutputModule:         "map_clocks",
			OutputModuleHash:     "7685a04836b6bac7ec654589bf1fe79ec3decbe1",
			TotalBlocksProcessed: 10,
			BlockRatePerSec:      "4.250",
			TimeToFirstData:      0.214910957,
			ResolvedStartBlock:   80830264,
			Error:                "context canceled",
			Timestamp:            "2026-02-12T18:01:38.610732316Z",
			Namespace:            "eth-bsc-mainnet",
			ClusterName:          "saas-us-central1",
			PodName:              "substreams-rpcv2-tier1-78c6987dc8-zlccb",
		},
		// Request 2: incoming
		{
			Message:          "incoming Substreams Blocks request",
			TraceID:          "aeff6e8e6a1178026a0f3d9b09f556e6",
			UserID:           "0bevuabef54becd7741bf",
			IPAddress:        "198.58.208.52",
			OutputModule:     "map_clocks",
			OutputModuleHash: "7685a04836b6bac7ec654589bf1fe79ec3decbe1",
			StartBlock:       -1,
			StopBlock:        0,
			ProductionMode:   false,
			Timestamp:        "2026-02-12T18:01:52.011004895Z",
			Namespace:        "eth-bsc-mainnet",
			ClusterName:      "saas-us-central1",
			PodName:          "substreams-rpcv2-tier1-78c6987dc8-fkb2r",
		},
		// Request 2: stats
		{
			Message:              "substreams request stats",
			Tier:                 "tier1",
			TraceID:              "aeff6e8e6a1178026a0f3d9b09f556e6",
			UserID:               "0bevuabef54becd7741bf",
			OutputModule:         "map_clocks",
			OutputModuleHash:     "7685a04836b6bac7ec654589bf1fe79ec3decbe1",
			TotalBlocksProcessed: 7,
			BlockRatePerSec:      "3.667",
			TimeToFirstData:      0.264035296,
			ResolvedStartBlock:   80830301,
			Error:                "context canceled",
			Timestamp:            "2026-02-12T18:01:53.868648606Z",
			Namespace:            "eth-bsc-mainnet",
			ClusterName:          "saas-us-central1",
			PodName:              "substreams-rpcv2-tier1-78c6987dc8-fkb2r",
		},
	}

	result := CorrelateConnections(entries)

	// Should have exactly 2 connections
	require.Equal(t, 2, len(result.Connections), "expected 2 connections from 4 entries")

	// No orphaned stats
	assert.Equal(t, 0, result.OrphanedCount, "expected no orphaned stats")

	// Both connections have "context canceled" which is a normal disconnect, not an error
	for _, conn := range result.Connections {
		assert.Equal(t, StatusClosed, conn.Status(), "connection %s should be closed (context canceled = normal disconnect)", conn.TraceID)
		require.NotNil(t, conn.Stats, "connection %s should have stats", conn.TraceID)
		assert.Equal(t, "context canceled", conn.Stats.Error)
	}

	// Verify specific connections
	connByTraceID := make(map[string]*ConnectionLog)
	for _, conn := range result.Connections {
		connByTraceID[conn.TraceID] = conn
	}

	// Check connection 1
	conn1 := connByTraceID["b8b020a93c7c48502103ede171a7844c"]
	require.NotNil(t, conn1, "connection 1 should exist")
	assert.Equal(t, "198.58.208.52", conn1.IPAddress)
	assert.Equal(t, "map_clocks", conn1.OutputModule)
	assert.Equal(t, "eth-bsc-mainnet", conn1.Namespace)
	assert.Equal(t, uint64(10), conn1.Stats.TotalBlocksProcessed)
	assert.Equal(t, "4.250", conn1.Stats.BlockRatePerSec)

	// Check connection 2
	conn2 := connByTraceID["aeff6e8e6a1178026a0f3d9b09f556e6"]
	require.NotNil(t, conn2, "connection 2 should exist")
	assert.Equal(t, "198.58.208.52", conn2.IPAddress)
	assert.Equal(t, "map_clocks", conn2.OutputModule)
	assert.Equal(t, "eth-bsc-mainnet", conn2.Namespace)
	assert.Equal(t, uint64(7), conn2.Stats.TotalBlocksProcessed)
	assert.Equal(t, "3.667", conn2.Stats.BlockRatePerSec)
}

// TestLogEntryDetectionRealData tests message detection with real data
func TestLogEntryDetectionRealData(t *testing.T) {
	incoming := LogEntry{
		Message: "incoming Substreams Blocks request",
	}
	assert.True(t, incoming.IsIncomingRequest())
	assert.False(t, incoming.IsRequestStats())

	statsTier1 := LogEntry{
		Message: "substreams request stats",
		Tier:    "tier1",
	}
	assert.False(t, statsTier1.IsIncomingRequest())
	assert.True(t, statsTier1.IsRequestStats())

	// Tier2 stats should be ignored
	statsTier2 := LogEntry{
		Message: "substreams request stats",
		Tier:    "tier2",
	}
	assert.False(t, statsTier2.IsRequestStats(), "tier2 stats should not be counted")
}
