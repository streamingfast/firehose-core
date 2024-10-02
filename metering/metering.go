package metering

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/streamingfast/dstore"

	"github.com/streamingfast/dmetering"
	"github.com/streamingfast/substreams/reqctx"
	"google.golang.org/protobuf/proto"
)

const (
	MeterLiveUncompressedReadBytes       = "live_uncompressed_read_bytes"
	MeterLiveUncompressedReadForkedBytes = "live_uncompressed_read_forked_bytes"

	MeterFileUncompressedReadBytes       = "file_uncompressed_read_bytes"
	MeterFileUncompressedReadForkedBytes = "file_uncompressed_read_forked_bytes"
	MeterFileCompressedReadForkedBytes   = "file_compressed_read_forked_bytes"
	MeterFileCompressedReadBytes         = "file_compressed_read_bytes"

	TotalReadBytes = "total_read_bytes"
)

func WithBlockBytesReadMeteringOptions(meter dmetering.Meter, logger *zap.Logger) []dstore.Option {
	return []dstore.Option{dstore.WithCompressedReadCallback(func(ctx context.Context, n int) {
		meter.CountInc(MeterFileCompressedReadBytes, n)
	})}
}

func WithForkedBlockBytesReadMeteringOptions(meter dmetering.Meter, logger *zap.Logger) []dstore.Option {
	return []dstore.Option{dstore.WithCompressedReadCallback(func(ctx context.Context, n int) {
		meter.CountInc(MeterFileCompressedReadForkedBytes, n)
	})}
}

func GetTotalBytesRead(meter dmetering.Meter) uint64 {
	total := uint64(meter.GetCount(TotalReadBytes))
	return total
}

func Send(ctx context.Context, meter dmetering.Meter, userID, apiKeyID, ip, userMeta, endpoint string, resp proto.Message) {
	bytesRead := meter.BytesReadDelta()
	bytesWritten := meter.BytesWrittenDelta()
	egressBytes := proto.Size(resp)

	liveUncompressedReadBytes := meter.GetCountAndReset(MeterLiveUncompressedReadBytes)
	liveUncompressedReadForkedBytes := meter.GetCountAndReset(MeterLiveUncompressedReadForkedBytes)

	fileUncompressedReadBytes := meter.GetCountAndReset(MeterFileUncompressedReadBytes)
	fileUncompressedReadForkedBytes := meter.GetCountAndReset(MeterFileUncompressedReadForkedBytes)
	fileCompressedReadForkedBytes := meter.GetCountAndReset(MeterFileCompressedReadForkedBytes)
	fileCompressedReadBytes := meter.GetCountAndReset(MeterFileCompressedReadBytes)

	totalReadBytes := fileCompressedReadBytes + fileCompressedReadForkedBytes + liveUncompressedReadBytes + liveUncompressedReadForkedBytes

	meter.CountInc(TotalReadBytes, int(totalReadBytes))

	event := dmetering.Event{
		UserID:    userID,
		ApiKeyID:  apiKeyID,
		IpAddress: ip,
		Meta:      userMeta,

		Endpoint: endpoint,
		Metrics: map[string]float64{
			"egress_bytes":                       float64(egressBytes),
			"written_bytes":                      float64(bytesWritten),
			"read_bytes":                         float64(bytesRead),
			MeterLiveUncompressedReadBytes:       float64(liveUncompressedReadBytes),
			MeterLiveUncompressedReadForkedBytes: float64(liveUncompressedReadForkedBytes),
			MeterFileUncompressedReadBytes:       float64(fileUncompressedReadBytes),
			MeterFileUncompressedReadForkedBytes: float64(fileUncompressedReadForkedBytes),
			MeterFileCompressedReadForkedBytes:   float64(fileCompressedReadForkedBytes),
			MeterFileCompressedReadBytes:         float64(fileCompressedReadBytes),
			"block_count":                        1,
		},
		Timestamp: time.Now(),
	}

	emitter := reqctx.Emitter(ctx)
	if emitter == nil {
		dmetering.Emit(context.WithoutCancel(ctx), event)
	} else {
		emitter.Emit(context.WithoutCancel(ctx), event)
	}
}
