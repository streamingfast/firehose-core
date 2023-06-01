package firecore

import (
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/logging"
	"github.com/streamingfast/node-manager/mindreader"
	pbbstream "github.com/streamingfast/pbgo/sf/bstream/v1"
	"go.uber.org/multierr"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// BlockPrinterFunc takes a chain agnostic [block] and prints it to a human readable form.
//
// See [ToolsConfig#BlockPrinter] for extra details about expected printing.
type BlockPrinterFunc func(block *bstream.Block, alsoPrintTransactions bool, out io.Writer) error

// Chain is the omni config object for configuring your chain specific information. It contains various
// fields that are used everywhere to properly configure the `firehose-<chain>` binary.
//
// Each field is documented about where it's used. Throughtout the different [Chain] option,
// we will use `Acme` as the chain's name placeholder, replace it with your chain name.
type Chain struct {
	// ShortName is the short name for your Firehose on <Chain> and is usually how
	// your chain's name is represented as a diminitutive. If your chain's name is already
	// short, we suggest to keep [ShortName] and [LongName] the same.
	//
	// As an example, Firehose on Ethereum [ShortName] is `eth` while Firehose on NEAR
	// short name is `near`.
	//
	// The [ShortName] **must** be  non-empty, lower cased and must **not** contain any spaces.
	ShortName string

	// LongName is the full name of your chain and the case sensitivy of this value is respected.
	// It is used in description of command and some logging output.
	//
	// The [LongName] **must** be non-empty.
	LongName string

	// Protocol is exactly 3 characters long that is going to identify your chain when writing blocks
	// to file. The written file contains an header and a part of this header is the protocol value.
	//
	// The [Protocol] **must** be non-empty and exactly 3 characters long all upper case.
	Protocol string

	// ProtocolVersion is the version of the protocol that is used to write blocks to file. This value
	// is used in the header of the written file. It should be changed each time the Protobuf model change
	// to become backward incompatible. This usually should be accompagnied by a change in the Protobuf
	// block model of the chain. For example for Ethereum we would go from `sf.ethereum.v1.Block` to
	// `sf.ethereum.v2.Block` and the [ProtocolVersion] would be incremented from `1` to `2`.
	//
	// The [ProtocolVersion] **must** be positive and non-zero and should be incremented each time the Protobuf model change.
	ProtocolVersion int32

	// ExecutableName is the name of the binary that is used to launch a syncing full node for this chain. For example,
	// on Ethereum, the binary by default is `geth`. This is used by the `reader-node` app to specify the
	// `reader-node-binary-name` flag.
	//
	// The [ExecutableName] **must** be non-empty.
	ExecutableName string

	// FullyQualifiedModule is the Go module of your actual `firehose-<chain>` repository and should
	// correspond to the `module` line of the `go.mod` file found at the root of your **own** `firehose-<chain>`
	// repository. The value can be seen using `head -1 go.mod | sed 's/module //'`.
	//
	// The [FullyQualifiedModule] **must** be non-empty.
	FullyQualifiedModule string

	// Version represents the actual version for your Firehose on <Chain>. It should be injected
	// via and `ldflags` through your `main` package.
	//
	// The [Version] **must** be non-empty.
	Version string

	// FirstStreamableBlock represents the block number of the first block that is streamable using Firehose,
	// for example on Ethereum it's set to `0`, the genesis block's number while on Antelope it's
	// set to 2 (genesis block is 1 there but our instrumentation on this chain instruments
	// only from block #2).
	//
	// This is used in multiple places to determine if we reached the oldest block of the chain.
	FirstStreamableBlock uint64

	// Should be the number of blocks between two targets before we consider the
	// first as "near" the second. For example if a chain is at block #215 and another
	// source is at block #225, then there is a difference of 10 blocks which is <=
	// than `BlockDifferenceThresholdConsideredNear` which would mean it's "near".
	//
	// Must be greater than 0 and lower than 1024
	BlockDifferenceThresholdConsideredNear uint64

	// BlockFactory is a factory function that returns a new instance of your chain's Block.
	// This new instance is usually used within `firecore` to unmarshal some bytes into your
	// chain's specific block model and return a [proto.Message] fully instantiated.
	//
	// The [BlockFactory] **must** be non-nil and must return a non-nil [proto.Message].
	BlockFactory func() Block

	// ConsoleReaderFactory is the function that should return the `ConsoleReader` that knowns
	// how to transform your your chain specific Firehose instrumentation logs into the proper
	// Block model of your chain.
	//
	// The [ConsoleReaderFactory] **must** be non-nil and must return a non-nil [mindreader.ConsolerReader] or an error.
	ConsoleReaderFactory func(lines chan string, blockEncoder BlockEncoder, logger *zap.Logger, tracer logging.Tracer) (mindreader.ConsolerReader, error)

	// Tools aggregate together all configuration options required for the various `fire<chain> tools`
	// to work properly for example to print block using chain specific information.
	//
	// The [Tools] element is optional and if not provided, sane defaults will be used.
	Tools *ToolsConfig

	// BlockEncoder is the cached block encoder object that should be used for this chain. Populate
	// when Init() is called.
	blockEncoder BlockEncoder
}

type ToolsConfig struct {
	// BlockPrinter represents a printing function that render a chain specific human readable
	// form of the receive chain agnostic [bstream.Block]. This block is expected to be rendered as
	// a single line for example on Ethereum rendering of a single block looks like:
	//
	// ```
	// Block #24924194 (01d6d349fbd3fa419182a2f0cf0b00714e101286650c239de8923caef6134b6c) 62 transactions, 607 calls
	// ```
	//
	// If the [alsoPrintTransactions] argument is true, each transaction of the block should also be printed, following
	// directly the block line. Each transaction should also be on a single line, usually prefixed with a `- ` to make
	// the rendering more appealing.
	//
	// For example on Ethereum rendering with [alsoPrintTransactions] being `true` looks like:
	//
	// ```
	// Block #24924194 (01d6d349fbd3fa419182a2f0cf0b00714e101286650c239de8923caef6134b6c) 62 transactions, 607 calls
	// - Transaction 0xc7e04240d6f2cc5f382c478fd0a0b5c493463498c64b31477b95bded8cd12ab4 (10 calls)
	// - Transaction 0xc7d8a698351eb1ac64acb76c8bf898365bb639865271add95d2c81650b2bd98c (4 calls)
	// ```
	//
	// The `out` parameter is used to write to the correct location. You can use [fmt.Fprintf] and [fmt.Fprintln]
	// and use `out` as the output writer in your implementation.
	//
	// The [BlockPrinter] is optional, if nil, a default block printer will be used. It's important to note
	// that the default block printer error out if `alsoPrintTransactions` is true.
	BlockPrinter BlockPrinterFunc
}

// Validate normalizes some aspect of the [Chain] values (spaces trimming essentially) and validates the chain
// by accumulating error an panic if all the error found along the way.
func (c *Chain) Validate() {
	c.ShortName = strings.ToLower(strings.TrimSpace(c.ShortName))
	c.LongName = strings.TrimSpace(c.LongName)
	c.Protocol = strings.ToLower(c.Protocol)
	c.ExecutableName = strings.TrimSpace(c.ExecutableName)

	var err error

	if c.ShortName == "" {
		err = multierr.Append(err, fmt.Errorf("field 'ShortName' must be non-empty"))
	}

	if strings.Contains(c.ShortName, " ") {
		err = multierr.Append(err, fmt.Errorf("field 'ShortName' must not contain any space(s)"))
	}

	if c.LongName == "" {
		err = multierr.Append(err, fmt.Errorf("field 'LongName' must be non-empty"))
	}

	if len(c.Protocol) != 3 {
		err = multierr.Append(err, fmt.Errorf("field 'Protocol' must be non-empty and have exactly 3 characters"))
	}

	if c.ProtocolVersion <= 0 {
		err = multierr.Append(err, fmt.Errorf("field 'ProtocolVersion' must be positive and non-zero"))
	}

	if c.ExecutableName == "" {
		err = multierr.Append(err, fmt.Errorf("field 'ExecutableName' must be non-empty"))
	}

	if c.FullyQualifiedModule == "" {
		err = multierr.Append(err, fmt.Errorf("field 'FullyQualifiedModule' must be non-empty"))
	}

	if c.Version == "" {
		err = multierr.Append(err, fmt.Errorf("field 'Version' must be non-empty"))
	}

	if c.BlockDifferenceThresholdConsideredNear == 0 {
		err = multierr.Append(err, fmt.Errorf("field 'BlockDifferenceThresholdConsideredNear' must be greater than 0"))
	}

	if c.BlockDifferenceThresholdConsideredNear > 1024 {
		err = multierr.Append(err, fmt.Errorf("field 'BlockDifferenceThresholdConsideredNear' must be lower than 1024"))
	}

	if c.BlockFactory == nil {
		err = multierr.Append(err, fmt.Errorf("field 'BlockFactory' must be non-nil"))
	} else if c.BlockFactory() == nil {
		err = multierr.Append(err, fmt.Errorf("field 'BlockFactory' must not produce nil blocks"))
	}

	if c.ConsoleReaderFactory == nil {
		err = multierr.Append(err, fmt.Errorf("field 'ConsoleReaderFactory' must be non-nil"))
	}

	errors := multierr.Errors(err)
	if len(errors) > 0 {
		errorLines := make([]string, len(errors))
		for i, err := range errors {
			errorLines[i] = fmt.Sprintf("- %s", err)
		}

		panic(fmt.Sprintf("firecore.Chain is invalid:\n%s", strings.Join(errorLines, "\n")))
	}
}

// Init is called when the chain is first loaded to initialize the `bstream`
// library with the chain specific configuration.
//
// This must called only once per chain per process.
//
// **Caveats** Two chain in the same Go binary will not work today as `bstream` uses global
// variables to store configuration which presents multiple chain to exist in the same process.
func (c *Chain) Init() {
	bstream.GetBlockWriterHeaderLen = 10
	bstream.GetMemoizeMaxAge = 20 * time.Second
	bstream.GetBlockPayloadSetter = bstream.MemoryBlockPayloadSetter

	bstream.GetBlockDecoder = bstream.BlockDecoderFunc(func(blk *bstream.Block) (any, error) {
		// blk.Kind() is not used anymore, only the content type and version is checked at read time now
		if blk.Version() != c.ProtocolVersion {
			return nil, fmt.Errorf("this decoder only knows about version %d, got %d", c.ProtocolVersion, blk.Version())
		}

		block := c.BlockFactory()
		payload, err := blk.Payload.Get()
		if err != nil {
			return nil, fmt.Errorf("getting payload: %w", err)
		}

		err = proto.Unmarshal(payload, block)
		if err != nil {
			return nil, fmt.Errorf("unable to decode payload: %w", err)
		}

		return block, nil
	})

	bstream.GetBlockWriterFactory = bstream.BlockWriterFactoryFunc(func(writer io.Writer) (bstream.BlockWriter, error) {
		return bstream.NewDBinBlockWriter(writer, c.Protocol, int(c.ProtocolVersion))
	})

	bstream.GetBlockReaderFactory = bstream.BlockReaderFactoryFunc(func(reader io.Reader) (bstream.BlockReader, error) {
		return bstream.NewDBinBlockReader(reader, func(contentType string, version int32) error {
			if contentType != c.Protocol && version != int32(c.ProtocolVersion) {
				return fmt.Errorf("reader only knows about %s block kind at version %d, got %s at version %d", c.Protocol, c.ProtocolVersion, contentType, version)
			}

			return nil
		})
	})

	c.blockEncoder = BlockEncoderFunc(func(b Block) (*bstream.Block, error) {
		content, err := proto.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal to binary form: %s", err)
		}

		block := &bstream.Block{
			Id:             b.GetFirehoseBlockID(),
			Number:         b.GetFirehoseBlockNumber(),
			PreviousId:     b.GetFirehoseBlockParentID(),
			Timestamp:      b.GetFirehoseBlockTime(),
			LibNum:         b.GetFirehoseBlockLIBNum(),
			PayloadVersion: c.ProtocolVersion,

			// PayloadKind is not actually used anymore and should be left to UNKNOWN
			PayloadKind: pbbstream.Protocol_UNKNOWN,
		}

		return bstream.GetBlockPayloadSetter(block, content)
	})
}

// BinaryName represents the binary name for your Firehose on <Chain> is the [ShortName]
// lowered appended to 'fire' prefix to before for example `fireacme`.
func (c *Chain) BinaryName() string {
	return "fire" + strings.ToLower(c.ShortName)
}

// RootLoggerPackageID is the `packageID` value when instantiating the root logger on the chain
// that is used by CLI command and other
func (c *Chain) RootLoggerPackageID() string {
	return c.LoggerPackageID(fmt.Sprintf("cmd/%s/cli", c.BinaryName()))
}

// LoggerPackageID computes a logger `packageID` value for a specific sub-package.
func (c *Chain) LoggerPackageID(subPackage string) string {
	return fmt.Sprintf("%s/%s", c.FullyQualifiedModule, subPackage)
}

// VersionString computes the version string that will be display when calling `firexxx --version`
// and extract build information from Git via Golang `debug.ReadBuildInfo`.
func (c *Chain) VersionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		panic("we should have been able to retrieve info from 'runtime/debug#ReadBuildInfo'")
	}

	commit := findSetting("vcs.revision", info.Settings)
	date := findSetting("vcs.time", info.Settings)

	var labels []string
	if len(commit) >= 7 {
		labels = append(labels, fmt.Sprintf("Commit %s", commit[0:7]))
	}

	if date != "" {
		labels = append(labels, fmt.Sprintf("Built %s", date))
	}

	if len(labels) == 0 {
		return c.Version
	}

	return fmt.Sprintf("%s (%s)", c.Version, strings.Join(labels, ", "))
}

func findSetting(key string, settings []debug.BuildSetting) (value string) {
	for _, setting := range settings {
		if setting.Key == key {
			return setting.Value
		}
	}

	return ""
}

func (c *Chain) BlockPrinter() BlockPrinterFunc {
	if c.Tools == nil || c.Tools.BlockPrinter == nil {
		return defaultBlockPrinter
	}

	return c.Tools.BlockPrinter
}

func defaultBlockPrinter(block *bstream.Block, alsoPrintTransactions bool, out io.Writer) error {
	if alsoPrintTransactions {
		return fmt.Errorf("transactions is not supported by the default block printer")
	}

	if _, err := fmt.Fprintf(out, "Block #%d (%s)\n", block.Number, block.Id); err != nil {
		return err
	}

	return nil
}
