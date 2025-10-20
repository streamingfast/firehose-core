package mindreader

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/derr"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/firehose/client"
	fcproto "github.com/streamingfast/firehose-core/proto"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type TestModeConfig struct {
	Enabled    bool
	AgainstDSN string
	DiffOutput string
}

type TestModeComparator struct {
	config      *TestModeConfig
	registry    *fcproto.Registry
	sanitizer   func(pbbstream *pbbstream.Block) *pbbstream.Block
	fetchClient pbfirehose.FetchClient
	closeFunc   func() error
	callOpts    []grpc.CallOption
	diffOutput  string
	diffStore   dstore.Store
	logger      *zap.Logger
}

func NewTestModeComparator(config *TestModeConfig, registry *fcproto.Registry, sanitizer func(pbbstream *pbbstream.Block) *pbbstream.Block, logger *zap.Logger) (*TestModeComparator, error) {
	if !config.Enabled {
		return nil, nil
	}

	if config.AgainstDSN == "" {
		return nil, fmt.Errorf("test mode enabled but no 'reader-node-test-mode-diff-against' DSN provided")
	}

	endpoint, apiKey, apiToken, insecure, plaintext, err := ParseTestModeDSN(config.AgainstDSN)
	if err != nil {
		return nil, fmt.Errorf("parse test mode DSN: %w", err)
	}

	fetchClient, closeFunc, callOpts, err := client.NewFirehoseFetchClient(endpoint, apiToken, apiKey, insecure, plaintext)
	if err != nil {
		return nil, fmt.Errorf("create firehose fetch client: %w", err)
	}

	diffOutput := config.DiffOutput
	if diffOutput == "" {
		diffOutput = "-"
	}

	var diffStore dstore.Store
	if diffOutput != "-" {
		diffStore, err = dstore.NewStore(diffOutput, "", "", false)
		if err != nil {
			closeFunc()
			return nil, fmt.Errorf("create diff output store: %w", err)
		}
	}

	return &TestModeComparator{
		config:      config,
		registry:    registry,
		sanitizer:   sanitizer,
		fetchClient: fetchClient,
		closeFunc:   closeFunc,
		callOpts:    callOpts,
		diffOutput:  diffOutput,
		diffStore:   diffStore,
		logger:      logger,
	}, nil
}

func (c *TestModeComparator) AgainstDSN() string {
	return c.config.AgainstDSN
}

func (c *TestModeComparator) DiffOutput() string {
	return c.config.DiffOutput
}

func (c *TestModeComparator) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	if c == nil {
		enc.AddBool("enabled", false)
		enc.AddString("against", "")
		enc.AddString("diff_output", "")
		return nil
	}
	enc.AddBool("enabled", true)
	enc.AddString("against", c.config.AgainstDSN)
	enc.AddString("diff_output", c.config.DiffOutput)
	return nil
}

func (c *TestModeComparator) Close() error {
	if c.closeFunc != nil {
		return c.closeFunc()
	}
	return nil
}

func (c *TestModeComparator) CompareBlock(ctx context.Context, testingBlock *pbbstream.Block) error {
	// Fetch the production block from the remote endpoint
	req := &pbfirehose.SingleBlockRequest{
		Reference: &pbfirehose.SingleBlockRequest_BlockHashAndNumber_{
			BlockHashAndNumber: &pbfirehose.SingleBlockRequest_BlockHashAndNumber{
				Num:  testingBlock.Number,
				Hash: testingBlock.Id,
			},
		},
	}

	var response *pbfirehose.SingleBlockResponse
	err := derr.Retry(3, func(ctx context.Context) (err error) {
		response, err = c.fetchClient.Block(ctx, req, c.callOpts...)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				c.logger.Debug("block not found on production endpoint, retrying",
					zap.Uint64("block_num", testingBlock.Number),
					zap.String("block_id", testingBlock.Id),
					zap.Error(err),
				)

				return fmt.Errorf("block not found on production endpoint, retrying")
			}

			return derr.NewFatalError(err)
		}

		return nil
	})
	if err != nil {
		c.logger.Warn("failed to fetch production block for comparison",
			zap.Uint64("block_num", testingBlock.Number),
			zap.String("block_id", testingBlock.Id),
			zap.Error(err),
		)

		return fmt.Errorf("failed to fetch production block: %w", err)
	}

	if response.Metadata.Num != testingBlock.Number {
		return fmt.Errorf("block number mismatch: testing %d vs production %d", testingBlock.Number, response.Metadata.Num)
	}

	if response.Metadata.Id != testingBlock.Id {
		// This could be due to a recent re-org, log and skip comparison
		c.logger.Info("block ID mismatch, possible re-org, skipping comparison",
			zap.Uint64("block_num", testingBlock.Number),
			zap.String("testing_block_id", testingBlock.Id),
			zap.String("production_block_id", response.Metadata.Id),
		)
		return nil
	}

	productionBlock := &pbbstream.Block{
		Number:    response.Metadata.Num,
		Id:        response.Metadata.Id,
		ParentId:  response.Metadata.ParentId,
		Timestamp: response.Metadata.Time,
		LibNum:    response.Metadata.LibNum,
		ParentNum: response.Metadata.ParentNum,
		Payload:   response.Block,
	}

	if c.sanitizer != nil {
		testingBlock = c.sanitizer(testingBlock)
		productionBlock = c.sanitizer(productionBlock)
	}

	testingChainBlock, err := c.registry.Unmarshal(testingBlock.Payload)
	if err != nil {
		return fmt.Errorf("unmarshal testing block payload: %w", err)
	}

	productionChainBlock, err := c.registry.Unmarshal(productionBlock.Payload)
	if err != nil {
		return fmt.Errorf("unmarshal production block payload: %w", err)
	}

	// Compare the chain-specific payload
	if proto.Equal(testingChainBlock, productionChainBlock) {
		fmt.Printf("✅ Block %d (%s) matches production\n", testingBlock.Number, testingBlock.Id)
		c.logger.Debug("blocks are identical", zap.Uint64("block_num", testingBlock.Number))
		return nil
	}

	// Payloads differ, generate diff (it's fine to use same block number and ID as this has been checked already here)
	return c.generateDiff(testingBlock.Number, testingBlock.Id, testingChainBlock, productionChainBlock)
}

func (c *TestModeComparator) generateDiff(
	blockNumber uint64,
	blockID string,
	testingChainBlock, productionChainBlock proto.Message,
) error {
	marshaler := protojson.MarshalOptions{
		Indent:            "  ",
		EmitDefaultValues: true,
	}

	testingJSON, err := marshaler.Marshal(testingChainBlock)
	if err != nil {
		return fmt.Errorf("marshal testing block to JSON: %w", err)
	}

	productionJSON, err := marshaler.Marshal(productionChainBlock)
	if err != nil {
		return fmt.Errorf("marshal production block to JSON: %w", err)
	}

	diff := generateUnifiedDiff(
		fmt.Sprintf("production.%d.%s.json", blockNumber, blockID),
		fmt.Sprintf("testing.%d.%s.json", blockNumber, blockID),
		string(productionJSON),
		string(testingJSON),
	)

	if c.diffOutput == "-" {
		fmt.Printf("⚠️  Diff found for block %d (%s)\n", blockNumber, blockID)
		fmt.Println(diff)
	} else {
		// Output to files via dstore
		ctx := context.Background()
		blockID := sanitizeBlockID(blockID)

		fmt.Printf("⚠️  Diff found for block %d (%s), writing to %s\n",
			blockNumber, blockID, c.diffOutput)

		testingPath := fmt.Sprintf("block.%d.%s.testing.json", blockNumber, blockID)
		if err := c.diffStore.WriteObject(ctx, testingPath, bytes.NewReader(testingJSON)); err != nil {
			return fmt.Errorf("write testing block JSON: %w", err)
		}

		productionPath := fmt.Sprintf("block.%d.%s.production.json", blockNumber, blockID)
		if err := c.diffStore.WriteObject(ctx, productionPath, bytes.NewReader(productionJSON)); err != nil {
			return fmt.Errorf("write production block JSON: %w", err)
		}

		diffPath := fmt.Sprintf("block.%d.%s.diff", blockNumber, blockID)
		if err := c.diffStore.WriteObject(ctx, diffPath, strings.NewReader(diff)); err != nil {
			return fmt.Errorf("write diff: %w", err)
		}
	}

	return nil
}

// generateUnifiedDiff generates a simple unified diff between two strings
func generateUnifiedDiff(fromName, toName, from, to string) string {
	var diff strings.Builder

	diff.WriteString(fmt.Sprintf("--- %s\n", fromName))
	diff.WriteString(fmt.Sprintf("+++ %s\n", toName))
	diff.WriteString("@@ Difference detected @@\n")

	fromLines := strings.Split(from, "\n")
	toLines := strings.Split(to, "\n")

	// Simple line-by-line comparison
	maxLines := len(fromLines)
	if len(toLines) > maxLines {
		maxLines = len(toLines)
	}

	for i := 0; i < maxLines; i++ {
		var fromLine, toLine string
		if i < len(fromLines) {
			fromLine = fromLines[i]
		}
		if i < len(toLines) {
			toLine = toLines[i]
		}

		if fromLine != toLine {
			if fromLine != "" {
				diff.WriteString(fmt.Sprintf("-%s\n", fromLine))
			}
			if toLine != "" {
				diff.WriteString(fmt.Sprintf("+%s\n", toLine))
			}
		}
	}

	return diff.String()
}

// sanitizeBlockID removes characters that are not safe for filenames
func sanitizeBlockID(blockID string) string {
	// Remove common prefixes and sanitize
	blockID = strings.TrimPrefix(blockID, "0x")
	blockID = strings.ReplaceAll(blockID, "/", "_")
	blockID = strings.ReplaceAll(blockID, "\\", "_")
	blockID = strings.ReplaceAll(blockID, ":", "_")

	// Truncate if too long
	if len(blockID) > 64 {
		blockID = blockID[:64]
	}

	return blockID
}

// ParseTestModeDSN parses the DSN format: http(s)://host:port[?insecure=true][&apiKey=key]
// Also checks FIREHOSE_API_KEY and FIREHOSE_API_TOKEN environment variables
func ParseTestModeDSN(dsn string) (endpoint string, apiKey string, apiToken string, insecure bool, plaintext bool, err error) {
	if dsn == "" {
		return "", "", "", false, false, nil
	}

	u, err := url.Parse(dsn)
	if err != nil {
		return "", "", "", false, false, fmt.Errorf("invalid DSN format: %w", err)
	}

	// Determine plaintext based on scheme
	if u.Scheme == "http" {
		plaintext = true
	} else if u.Scheme == "https" {
		plaintext = false
	} else {
		return "", "", "", false, false, fmt.Errorf("DSN must start with http:// or https://")
	}

	endpoint = dsn

	// Parse query parameters
	query := u.Query()
	if query.Get("insecure") == "true" {
		insecure = true
	}
	if key := query.Get("apiKey"); key != "" {
		apiKey = key
	}

	// Check environment variables
	if apiKey == "" {
		apiKey = os.Getenv("FIREHOSE_API_KEY")
	}
	apiToken = os.Getenv("FIREHOSE_API_TOKEN")

	return endpoint, apiKey, apiToken, insecure, plaintext, nil
}
