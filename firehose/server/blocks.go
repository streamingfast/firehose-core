package server

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	connect "connectrpc.com/connect"
	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/bstream/stream"
	"github.com/streamingfast/dauth"
	"github.com/streamingfast/dmetering"
	"github.com/streamingfast/dsession"
	"github.com/streamingfast/firehose-core/firehose/metrics"
	"github.com/streamingfast/firehose-core/metering"
	"github.com/streamingfast/logging"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	tracing "github.com/streamingfast/sf-tracing"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func (s *Server) Block(ctx context.Context, request *connect.Request[pbfirehose.SingleBlockRequest]) (*connect.Response[pbfirehose.SingleBlockResponse], error) {
	var blockNum uint64
	var blockHash string
	switch ref := request.Msg.Reference.(type) {
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

	return connect.NewResponse(resp), nil
}

// Blocks(context.Context, *connect.Request[v2.Request], *connect.ServerStream[v2.Response]) error
func (s *Server) Blocks(ctx context.Context, request *connect.Request[pbfirehose.Request], streamSrv *connect.ServerStream[pbfirehose.Response]) (err error) {
	metrics.RequestCounter.Inc()

	ctx, cancelRunning := context.WithCancelCause(ctx)
	defer cancelRunning(nil)

	requestStart := time.Now()
	logger := logging.LoggerFromContext(ctx, s.logger)

	auth := dauth.FromContext(ctx)
	var organizationID, apiKeyID, realIP string
	if auth != nil {
		organizationID = auth.OrganizationID()
		apiKeyID = auth.APIKeyID()
		realIP = auth.RealIP()
	}

	logger.Info("incoming firehose Blocks request",
		zap.String("organization_id", organizationID),
		zap.String("api_key_id", apiKeyID),
		zap.String("real_ip", realIP),
		zap.Int64("start_block", request.Msg.StartBlockNum),
		zap.Uint64("stop_block", request.Msg.StopBlockNum),
		zap.Bool("final_blocks_only", request.Msg.FinalBlocksOnly),
		zap.String("cursor", request.Msg.Cursor),
	)

	var (
		firstDataSent      bool
		timeToFirstData    time.Duration
		resolvedStartBlock uint64
		streamRan          bool
		runErr             error
	)

	defer func() {
		logErr := err
		if streamRan {
			logErr = runErr
		}
		if errors.Is(logErr, context.Canceled) {
			logErr = context.Canceled
		}

		meter := getRequestMeter(ctx)
		fields := []zap.Field{
			zap.Uint64("block_sent", meter.blocks),
			zap.Int("egress_bytes", meter.egressBytes),
			zap.Duration("duration", time.Since(requestStart)),
			zap.Error(logErr),
		}
		if firstDataSent {
			fields = append(fields,
				zap.Duration("time_to_first_data", timeToFirstData),
				zap.Uint64("resolved_start_block", resolvedStartBlock),
			)
		}
		if auth != nil {
			fields = append(fields,
				zap.String("api_key_id", apiKeyID),
				zap.String("user_id", organizationID),
				zap.String("real_ip", realIP),
			)
		}

		logger.Info("firehose process completed", fields...)
	}()

	if s.sessionPool != nil {
		service := "t1r"
		traceID := tracing.GetTraceID(ctx).String()

		sessionID, err := s.sessionPool.Get(ctx, service, organizationID, apiKeyID, traceID, func(err error) {
			if cancelRunning != nil { // in tests, this might be nil
				err, _ = dsession.ToConnectError(err)
				cancelRunning(err)
			}
		})

		if err != nil {
			switch {
			case errors.Is(err, dsession.ErrConcurrentStreamLimitExceeded),
				errors.Is(err, dsession.ErrPermissionDenied),
				errors.Is(err, dsession.ErrQuotaExceeded):
				incrementFirehoseSessionDeniedCounter(err)
				logger.Debug("session denied to user", zap.String("user_id", organizationID), zap.String("api_key_id", apiKeyID), zap.Error(err))
			default:
				logger.Error("failed to acquire session", zap.Error(err), zap.String("service", service), zap.String("user_id", organizationID), zap.String("api_key_id", apiKeyID))
			}
			return err
		}

		logger.Debug("acquired session", zap.String("session_id", sessionID))

		defer func() {
			logger.Debug("releasing session", zap.String("session_id", sessionID))
			s.sessionPool.Release(sessionID)
		}()
	}

	if !matchHeader(request.Header()) {
		if s.enforceCompression {
			return status.Error(codes.InvalidArgument, "client does not support compression")
		}
		logger.Info("client does not support compression")
	}

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
		streamSrv.ResponseHeader().Add("hostname", hostname)
	}

	lastSentCursor := ""
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

		protoStep, skip := stepToProto(step, request.Msg.FinalBlocksOnly)
		if skip {
			return nil
		}

		cur := cursor.ToOpaque()
		if step == bstream.StepPartial {
			cur = lastSentCursor // never send the cursor from a 'partial' step because we can't re-connect to it
		} else {
			lastSentCursor = cur
		}
		if cur == "" {
			return nil // don't send partial blocks until we've had at least one cursor
		}
		resp := &pbfirehose.Response{
			Step:   protoStep,
			Cursor: cur,
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

		if !firstDataSent {
			firstDataSent = true
			timeToFirstData = time.Since(requestStart)
			resolvedStartBlock = block.Number
		}

		level := zap.DebugLevel
		if block.Number%200 == 0 {
			level = zap.InfoLevel
		}

		logger.Check(level, "stream sent block").Write(zap.Uint64("block_num", block.Number), zap.String("block_id", block.Id), zap.Duration("duration", time.Since(start)))

		return nil
	})

	if len(request.Msg.Transforms) > 0 && s.transformRegistry == nil {
		return status.Errorf(codes.Unimplemented, "no transforms registry configured within this instance")
	}

	liveSourceMiddlewareHandler := func(next bstream.Handler) bstream.Handler {
		return bstream.HandlerFunc(func(blk *pbbstream.Block, obj interface{}) error {
			var isNew bool
			if stepable, ok := obj.(bstream.Stepable); ok {
				if stepable.Step().Matches(bstream.StepNew | bstream.StepPartial) {
					isNew = true
					dmetering.GetBytesMeter(ctx).CountInc(metering.MeterLiveUncompressedReadBytes, len(blk.GetPayload().GetValue()))

					// legacy metering
					// todo(colin): remove this once we are sure the new metering is working
					dmetering.GetBytesMeter(ctx).AddBytesRead(len(blk.GetPayload().GetValue()))
				} else {
					dmetering.GetBytesMeter(ctx).CountInc(metering.MeterLiveUncompressedReadForkedBytes, len(blk.GetPayload().GetValue()))
				}
			}
			err := next.ProcessBlock(blk, obj)
			if err != nil {
				return err
			}

			// metrics for sent live block
			if liveable, ok := obj.(bstream.Liveable); ok && isNew {
				if liveable.IsLiveBlock() {
					metrics.FirehoseOutputHeadBlockRelativeTime.SetLastBlock(blk.Time())
				}
			}

			return nil
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

	ctx = s.initFunc(ctx, request.Msg)
	stepFilter := bstream.StepNew | bstream.StepUndo
	str, err := s.streamFactory.New(
		ctx,
		handlerFunc,
		request.Msg,
		logger,
		stream.WithLiveSourceHandlerMiddleware(liveSourceMiddlewareHandler),
		stream.WithFileSourceHandlerMiddleware(fileSourceMiddlewareHandler),
		stream.WithCustomStepTypeFilter(stepFilter),
	)
	if err != nil {
		return err
	}

	runErr = str.Run(ctx)
	streamRan = true

	if runErr != nil {
		if errors.Is(runErr, stream.ErrStopBlockReached) {
			logger.Info("stream of blocks reached end block")
			runErr = nil // successful completion, not an error worth surfacing in the completion log
			return nil
		}

		if errors.Is(runErr, context.Canceled) {
			if ctx.Err() != context.Canceled {
				logger.Debug("stream of blocks ended with context canceled, but our own context was not canceled", zap.Error(runErr))
			}
			if causeErr, ok := context.Cause(ctx).(*connect.Error); ok {
				return causeErr
			}
			return status.Error(codes.Canceled, "source canceled")
		}

		if errors.Is(runErr, context.DeadlineExceeded) {
			logger.Info("stream of blocks ended with context deadline exceeded", zap.Error(runErr))
			return status.Error(codes.DeadlineExceeded, "source deadline exceeded")
		}

		if errUnavailable, ok := errors.AsType[*stream.ErrUnavailable](runErr); ok {
			// The request is fine, this instance cannot serve it: the client should
			// retry, here or on another instance, keeping its cursor.
			return status.Error(codes.Unavailable, errUnavailable.Error())
		}

		if errInvalidArg, ok := errors.AsType[*stream.ErrInvalidArg](runErr); ok {
			return status.Error(codes.InvalidArgument, errInvalidArg.Error())
		}

		if errSendBlock, ok := errors.AsType[*ErrSendBlock](runErr); ok {
			logger.Info("unable to send block probably due to client disconnecting", zap.Error(errSendBlock.inner))
			return status.Error(codes.Unavailable, errSendBlock.inner.Error())
		}

		logger.Info("unexpected stream of blocks termination", zap.Error(runErr))
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
	if step.Matches(bstream.StepUndo) || step.Matches(bstream.StepUndoPartial) {
		return pbfirehose.ForkStep_STEP_UNDO, false
	}
	return 0, true // simply skip irreversible or stalled here
}

// must be lowercase
var compressionHeader = map[string]map[string]bool{
	"grpc-accept-encoding":    {"gzip": true, "zstd": true},
	"connect-accept-encoding": {"gzip": true, "zstd": true},
	"accept-encoding":         {"gzip": true}, // HTTP encoding for connect+proto in browser
}

func matchHeader(header http.Header) bool {
	for k, v := range header {
		if validEncodings, ok := compressionHeader[strings.ToLower(k)]; ok {
			for _, vv := range v {
				for _, vvv := range strings.Split(vv, ",") {
					if validEncodings[strings.TrimSpace(strings.ToLower(vvv))] {
						return true
					}
				}
			}
		}
	}
	return false
}

func incrementFirehoseSessionDeniedCounter(err error) {
	errorToReason := func() string {
		switch {
		case errors.Is(err, dsession.ErrPermissionDenied):
			return "permission_denied"
		case errors.Is(err, dsession.ErrQuotaExceeded):
			return "quota_exceeded"
		case errors.Is(err, dsession.ErrConcurrentStreamLimitExceeded):
			return "concurrent_stream_limit_exceeded"
		default:
			return "unknown"
		}
	}()

	metrics.FirehoseSessionDeniedCounter.Inc(errorToReason)
}
