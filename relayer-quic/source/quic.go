// Copyright 2019 dfuse Platform Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package source

import (
	"block-streamer/blockinfo"
	"block-streamer/quic"
	"context"
	"crypto/tls"
	"fmt"
	"io"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type QuicSource struct {
	*shutter.Shutter
	addr            string
	upstreamHandler bstream.Handler
	logger          *zap.Logger
}

func NewQuicSource(ctx context.Context, addr string, upstreamHandler bstream.Handler, logger *zap.Logger) *QuicSource {
	s := &QuicSource{
		Shutter:         shutter.New(),
		addr:            addr,
		upstreamHandler: upstreamHandler,
		logger:          logger,
	}

	go func() {
		select {
		case <-ctx.Done():
			s.Shutdown(ctx.Err())
		case <-s.Terminating():
		}
	}()

	return s
}

func (s *QuicSource) Run() {
	s.logger.Info("starting source, connecting to quic server", zap.String("addr", s.addr))

	// For now, we skip TLS verification for the client because we use self-signed certs.
	// In a real environment, we'd want to provide a proper tls.Config.
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"block-streamer"},
	}

	client := quic.NewClient(tlsConfig, func(info *blockinfo.BlockInfo, payload io.Reader) error {
		data, err := io.ReadAll(payload)
		if err != nil {
			return fmt.Errorf("reading payload: %w", err)
		}

		s.logger.Debug("received block info: ", zap.String("server_address", s.addr), zap.Any("block_info", info))

		if uint64(len(data)) != info.PayloadSize {
			return fmt.Errorf("payload size mismatch, expected %d bytes, got %d", info.PayloadSize, uint64(len(data)))
		}

		blk := &pbbstream.Block{
			Id:        info.ID,
			Number:    info.Number,
			ParentId:  info.ParentID,
			ParentNum: info.ParentNum,
			LibNum:    info.LibNum,
			Timestamp: timestamppb.New(info.Timestamp),
			Payload: &anypb.Any{
				TypeUrl: "type.googleapis.com/block-streamer.S2CompressedData",
				Value:   data,
			},
		}

		if err := s.upstreamHandler.ProcessBlock(blk, nil); err != nil {
			return fmt.Errorf("processing block: %w", err)
		}

		return nil
	})

	err := client.Connect(context.Background(), s.addr)
	if err != nil {
		s.logger.Error("failed to connect to quic server", zap.String("addr", s.addr), zap.Error(err))
		s.Shutdown(err)
		return
	}

	s.logger.Info("connected to quic server", zap.String("addr", s.addr))
}
