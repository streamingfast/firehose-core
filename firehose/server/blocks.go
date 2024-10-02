package server

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/bstream/stream"
	"github.com/streamingfast/dauth"
	"github.com/streamingfast/dmetering"
	"github.com/streamingfast/firehose-core/firehose/metrics"
	"github.com/streamingfast/firehose-core/metering"
	"github.com/streamingfast/logging"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func (s *Server) Block(ctx context.Context, request *pbfirehose.SingleBlockRequest) (*pbfirehose.SingleBlockResponse, error) {
	var blockNum uint64
	var blockHash string
	switch ref := request.Reference.(type) {
	case *pbfirehose.SingleBlockRequest_BlockHashAndNumber_:
		blockNum = ref.BlockHashAndNumber.Num
		blockHash = ref.BlockHashAndNumber.Hash
	case *pbfirehose.SingleBlockRequest_Cursor_:
		cur, err := bstream.CursorFromOpaque(ref.Cursor.Cursor)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		blockNum = cur.Block.Num()
		blockHash = cur.Block.ID()
	case *pbfirehose.SingleBlockRequest_BlockNumber_:
		blockNum = ref.BlockNumber.Num
	}

	ctx = dmetering.WithBytesMeter(ctx)
	blk, err := s.blockGetter.Get(ctx, blockNum, blockHash, s.logger)
	if err != nil {
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	if blk == nil {
		return nil, status.Errorf(codes.NotFound, "block %s not found", bstream.NewBlockRef(blockHash, blockNum))
	}

	resp := &pbfirehose.SingleBlockResponse{
		Block: blk.Payload,
		Metadata: &pbfirehose.BlockMetadata{
			Id:        blk.Id,
			Num:       blk.Number,
			ParentId:  blk.ParentId,
			ParentNum: blk.ParentNum,
			LibNum:    blk.LibNum,
			Time:      blk.Timestamp,
		},
	}

	meter := dmetering.GetBytesMeter(ctx)
	auth := dauth.FromContext(ctx)
	metering.Send(ctx, meter, auth.UserID(), auth.APIKeyID(), auth.RealIP(), auth.Meta(), "sf.firehose.v2.Firehose/Block", resp)

	return resp, nil
}

func (s *Server) Blocks(request *pbfirehose.Request, streamSrv pbfirehose.Stream_BlocksServer) error {
	ctx := streamSrv.Context()
	metrics.RequestCounter.Inc()

	logger := logging.Logger(ctx, s.logger)

	if s.rateLimiter != nil {
		rlCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		if allow := s.rateLimiter.Take(rlCtx, "", "Blocks"); !allow {
			jitterDelay := time.Duration(rand.Intn(3000) + 1000) // force a minimal backoff
			<-time.After(time.Millisecond * jitterDelay)
			return status.Error(codes.Unavailable, "rate limit exceeded")
		} else {
			defer s.rateLimiter.Return()
		}
	}

	metrics.ActiveRequests.Inc()
	defer metrics.ActiveRequests.Dec()

	if os.Getenv("FIREHOSE_SEND_HOSTNAME") != "" {
		hostname, err := os.Hostname()
		if err != nil {
			hostname = "unknown"
			logger.Warn("cannot determine hostname, using 'unknown'", zap.Error(err))
		}
		md := metadata.New(map[string]string{"hostname": hostname})
		if err := streamSrv.SendHeader(md); err != nil {
			logger.Warn("cannot send metadata header", zap.Error(err))
		}
	}

	var blockCount uint64
	handlerFunc := bstream.HandlerFunc(func(block *pbbstream.Block, obj interface{}) error {
		blockCount++
		cursorable := obj.(bstream.Cursorable)
		cursor := cursorable.Cursor()

		stepable := obj.(bstream.Stepable)
		step := stepable.Step()

		wrapped := obj.(bstream.ObjectWrapper)
		obj = wrapped.WrappedObject()
		if obj == nil {
			obj = block.Payload
		}

		protoStep, skip := stepToProto(step, request.FinalBlocksOnly)
		if skip {
			return nil
		}

		resp := &pbfirehose.Response{
			Step:   protoStep,
			Cursor: cursor.ToOpaque(),
			Metadata: &pbfirehose.BlockMetadata{
				Id:        block.Id,
				Num:       block.Number,
				ParentId:  block.ParentId,
				ParentNum: block.ParentNum,
				LibNum:    block.LibNum,
				Time:      block.Timestamp,
			},
		}

		switch v := obj.(type) {
		case *anypb.Any:
			resp.Block = v
		case proto.Message:
			cnt, err := anypb.New(v)
			if err != nil {
				return fmt.Errorf("to any: %w", err)
			}
			resp.Block = cnt
		default:
			// this can be the out
			return fmt.Errorf("unknown object type %t, cannot marshal to protobuf Any", v)
		}

		if s.postHookFunc != nil {
			s.postHookFunc(ctx, resp)
		}
		start := time.Now()
		err := streamSrv.Send(resp)
		if err != nil {
			logger.Info("stream send error", zap.Uint64("block_num", block.Number), zap.String("block_id", block.Id), zap.Error(err))
			return NewErrSendBlock(err)
		}

		level := zap.DebugLevel
		if block.Number%200 == 0 {
			level = zap.InfoLevel
		}

		logger.Check(level, "stream sent block").Write(zap.Uint64("block_num", block.Number), zap.String("block_id", block.Id), zap.Duration("duration", time.Since(start)))

		return nil
	})

	if len(request.Transforms) > 0 && s.transformRegistry == nil {
		return status.Errorf(codes.Unimplemented, "no transforms registry configured within this instance")
	}

	liveSourceMiddlewareHandler := func(next bstream.Handler) bstream.Handler {
		return bstream.HandlerFunc(func(blk *pbbstream.Block, obj interface{}) error {
			if stepable, ok := obj.(bstream.Stepable); ok {
				if stepable.Step().Matches(bstream.StepNew) {
					dmetering.GetBytesMeter(ctx).CountInc(metering.MeterLiveUncompressedReadBytes, len(blk.GetPayload().GetValue()))

					// legacy metering
					// todo(colin): remove this once we are sure the new metering is working
					dmetering.GetBytesMeter(ctx).AddBytesRead(len(blk.GetPayload().GetValue()))
				} else {
					dmetering.GetBytesMeter(ctx).CountInc(metering.MeterLiveUncompressedReadForkedBytes, len(blk.GetPayload().GetValue()))
				}
			}
			return next.ProcessBlock(blk, obj)
		})
	}

	fileSourceMiddlewareHandler := func(next bstream.Handler) bstream.Handler {
		return bstream.HandlerFunc(func(blk *pbbstream.Block, obj interface{}) error {
			if stepable, ok := obj.(bstream.Stepable); ok {
				if stepable.Step().Matches(bstream.StepNew) {
					dmetering.GetBytesMeter(ctx).CountInc(metering.MeterFileUncompressedReadBytes, len(blk.GetPayload().GetValue()))
				} else {
					dmetering.GetBytesMeter(ctx).CountInc(metering.MeterFileUncompressedReadForkedBytes, len(blk.GetPayload().GetValue()))
				}
			}
			return next.ProcessBlock(blk, obj)
		})
	}

	ctx = s.initFunc(ctx, request)
	str, err := s.streamFactory.New(
		ctx,
		handlerFunc,
		request,
		logger,
		stream.WithLiveSourceHandlerMiddleware(liveSourceMiddlewareHandler),
		stream.WithFileSourceHandlerMiddleware(fileSourceMiddlewareHandler),
	)
	if err != nil {
		return err
	}

	err = str.Run(ctx)
	meter := getRequestMeter(ctx)

	fields := []zap.Field{
		zap.Uint64("block_sent", meter.blocks),
		zap.Int("egress_bytes", meter.egressBytes),
		zap.Error(err),
	}

	auth := dauth.FromContext(ctx)
	if auth != nil {
		fields = append(fields,
			zap.String("api_key_id", auth.APIKeyID()),
			zap.String("user_id", auth.UserID()),
			zap.String("real_ip", auth.RealIP()),
		)
	}
	logger.Info("firehose process completed", fields...)
	if err != nil {
		if errors.Is(err, stream.ErrStopBlockReached) {
			logger.Info("stream of blocks reached end block")
			return nil
		}

		if errors.Is(err, context.Canceled) {
			if ctx.Err() != context.Canceled {
				logger.Debug("stream of blocks ended with context canceled, but our own context was not canceled", zap.Error(err))
			}
			return status.Error(codes.Canceled, "source canceled")
		}

		if errors.Is(err, context.DeadlineExceeded) {
			logger.Info("stream of blocks ended with context deadline exceeded", zap.Error(err))
			return status.Error(codes.DeadlineExceeded, "source deadline exceeded")
		}

		var errInvalidArg *stream.ErrInvalidArg
		if errors.As(err, &errInvalidArg) {
			return status.Error(codes.InvalidArgument, errInvalidArg.Error())
		}

		var errSendBlock *ErrSendBlock
		if errors.As(err, &errSendBlock) {
			logger.Info("unable to send block probably due to client disconnecting", zap.Error(errSendBlock.inner))
			return status.Error(codes.Unavailable, errSendBlock.inner.Error())
		}

		logger.Info("unexpected stream of blocks termination", zap.Error(err))
		return status.Errorf(codes.Internal, "unexpected stream termination")
	}

	logger.Error("source is not expected to terminate gracefully, should stop at block or continue forever")
	return status.Error(codes.Internal, "unexpected stream completion")

}

func stepToProto(step bstream.StepType, finalBlocksOnly bool) (outStep pbfirehose.ForkStep, skip bool) {
	if finalBlocksOnly {
		if step.Matches(bstream.StepIrreversible) {
			return pbfirehose.ForkStep_STEP_FINAL, false
		}
		return 0, true
	}

	if step.Matches(bstream.StepNew) {
		return pbfirehose.ForkStep_STEP_NEW, false
	}
	if step.Matches(bstream.StepUndo) {
		return pbfirehose.ForkStep_STEP_UNDO, false
	}
	return 0, true // simply skip irreversible or stalled here
}
