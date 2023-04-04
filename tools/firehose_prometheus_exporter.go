package tools

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/firehose/client"
	"github.com/streamingfast/logging"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/anypb"
)

var status = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "firehose_healthcheck_status", Help: "Either 1 for successful firehose request, or 0 for failure"}, []string{"endpoint"})
var propagationDelay = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "firehose_healthcheck_block_delay", Help: "Delay between block time and propagation to firehose clients"}, []string{"endpoint"})

var lastBlockLock sync.Mutex
var lastBlockReceived time.Time
var driftSec = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "firehose_healthcheck_drift", Help: "Time since the most recent block received (seconds)"}, []string{"endpoint"})

// You should add your custom 'transforms' flags to this command in your init(), then parse them in transformsSetter
var GetFirehosePrometheusExporterCmd = func(zlog *zap.Logger, tracer logging.Tracer, transformsSetter TransformsSetter) *cobra.Command {
	out := &cobra.Command{
		Use:   "firehose-prometheus-exporter <endpoint:port>",
		Short: "stream blocks near the chain HEAD and report to prometheus",
		Args:  cobra.ExactArgs(1),
		RunE:  runPrometheusExporterE(zlog, tracer, transformsSetter),
	}
	out.Flags().StringP("api-token-env-var", "a", "FIREHOSE_API_TOKEN", "Look for a JWT in this environment variable to authenticate against endpoint")
	//out.Flags().String("cursor-path", "", "if not-empty, save cursor to this location and read from there on restart")
	out.Flags().BoolP("plaintext", "p", false, "Use plaintext connection to firehose")
	out.Flags().BoolP("insecure", "k", false, "Skip SSL certificate validation when connecting to firehose")

	return out
}

func runPrometheusExporterE(zlog *zap.Logger, tracer logging.Tracer, transformsSetter TransformsSetter) func(cmd *cobra.Command, args []string) error {

	return func(cmd *cobra.Command, args []string) error {

		ctx := context.Background()
		endpoint := args[0]

		start := int64(-1)
		stop := uint64(0)
		apiTokenEnvVar := mustGetString(cmd, "api-token-env-var")
		jwt := os.Getenv(apiTokenEnvVar)

		plaintext := mustGetBool(cmd, "plaintext")
		insecure := mustGetBool(cmd, "insecure")

		firehoseClient, connClose, grpcCallOpts, err := client.NewFirehoseClient(endpoint, jwt, insecure, plaintext)
		if err != nil {
			return err
		}
		defer connClose()

		var transforms []*anypb.Any
		if transformsSetter != nil {
			transforms, err = transformsSetter(cmd)
			if err != nil {
				return err
			}
		}

		request := &pbfirehose.Request{
			StartBlockNum:   start,
			StopBlockNum:    stop,
			Transforms:      transforms,
			FinalBlocksOnly: false,
			//	Cursor:          cursor,
		}

		prometheus.MustRegister(status)
		prometheus.MustRegister(propagationDelay)
		prometheus.MustRegister(driftSec)

		// update the drift based on last time
		go func() {
			for {
				time.Sleep(500 * time.Millisecond)
				lastBlockLock.Lock()
				driftSec.With(prometheus.Labels{"endpoint": endpoint}).Set(time.Since(lastBlockReceived).Seconds())
				lastBlockLock.Unlock()
			}
		}()

		var sleepTime time.Duration
		for {
			time.Sleep(sleepTime)
			sleepTime = time.Second * 3
			stream, err := firehoseClient.Blocks(ctx, request, grpcCallOpts...)
			if err != nil {
				zlog.Error("connecting", zap.Error(err))
				markFailure(endpoint)
				continue
			}

			zlog.Info("connected")

			for {
				response, err := stream.Recv()
				if err != nil {
					zlog.Error("got error from stream", zap.Error(err))
					markFailure(endpoint)
					break
				}

				if cursor, err := bstream.CursorFromOpaque(response.Cursor); err == nil {
					zlog.Info("Got block", zap.Stringer("block", cursor.Block))

					lastBlockLock.Lock()
					lastBlockReceived = time.Now()
					lastBlockLock.Unlock()
					markSuccess(endpoint)
				}
			}

		}

		//	serve := http.Server{Handler: handler, Addr: addr}
		//	if err := serve.ListenAndServe(); err != nil {
		//		zlog.Error("can't listen on the metrics endpoint", zap.Error(err))
		//		return err
		//	}
		//	return nil

		return nil
	}
}

func markSuccess(endpoint string) {
	status.With(prometheus.Labels{"endpoint": endpoint}).Set(1)
}

func markFailure(endpoint string) {
	status.With(prometheus.Labels{"endpoint": endpoint}).Set(0)
}
