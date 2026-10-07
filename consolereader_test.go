package firecore

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/streamingfast/firehose-core/test"
	"github.com/streamingfast/logging"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
)

var zlogTest, tracerTest = logging.PackageLogger("test", "github.com/streamingfast/firehose-core/firecore")

func Test_Ctx_readBlock(t *testing.T) {
	reader := &ConsoleReader{
		logger: zlogTest,
		tracer: tracerTest,

		readerProtocolVersion: "1.0",
		protoMessageType:      "type.googleapis.com/sf.ethereum.type.v2.Block",
	}

	blockHash := "d2836a703a02f3ca2a13f05efe26fc48c6fa0db0d754a49e56b066d3b7d54659"
	blockHashBytes, err := hex.DecodeString(blockHash)
	blockNumber := uint64(18571000)

	parentHash := "55de88c909fa368ae1e93b6b8ffb3fbb12e64aefec1d4a1fcc27ae7633de2f81"
	parentBlockNumber := 18570999

	libNumber := 18570800

	pbBlock := test.Block{
		Hash:   blockHashBytes,
		Number: blockNumber,
	}

	anypbBlock, err := anypb.New(&pbBlock)

	require.NoError(t, err)
	nowNano := time.Now().UnixNano()
	line := fmt.Sprintf(
		"%d %s %d %s %d %d %s",
		blockNumber,
		blockHash,
		parentBlockNumber,
		parentHash,
		libNumber,
		nowNano,
		base64.StdEncoding.EncodeToString(anypbBlock.Value),
	)

	block, err := reader.readBlock(line)
	require.NoError(t, err)

	require.Equal(t, blockNumber, block.Number)
	require.Equal(t, blockHash, block.Id)
	require.Equal(t, parentHash, block.ParentId)
	require.Equal(t, uint64(libNumber), block.LibNum)
	require.Equal(t, int32(time.Unix(0, nowNano).Nanosecond()), block.Timestamp.Nanos)

	require.NoError(t, err)
	require.Equal(t, anypbBlock.GetValue(), block.Payload.Value)

}

func Test_resolveBlockType(t *testing.T) {
	tests := []struct {
		name          string
		typeOrVariant string
		expectType    string
		expectVariant string
		expectVersion string
		expectErr     string
	}{
		{
			name:          "ethereum fully qualified name",
			typeOrVariant: "sf.ethereum.type.v2.Block",
			expectType:    "sf.ethereum.type.v2.Block",
		},
		{
			name:          "cosmos fully qualified name",
			typeOrVariant: "sf.cosmos.type.v2.Block",
			expectType:    "sf.cosmos.type.v2.Block",
		},
		{
			name:          "geth variant older version",
			typeOrVariant: "geth 1.16.3",
			expectType:    "sf.ethereum.type.v2.Block",
			expectVariant: "geth",
			expectVersion: "1.16.3",
		},
		{
			name:          "geth variant newer version",
			typeOrVariant: "geth 1.20.4",
			expectType:    "sf.ethereum.type.v2.Block",
			expectVariant: "geth",
			expectVersion: "1.20.4",
		},
		{
			name:          "polygon variant with complex version string",
			typeOrVariant: "polygon 1.10.17-fh+hotfix (deadbeef) built-by-ci",
			expectType:    "sf.ethereum.type.v2.Block",
			expectVariant: "polygon",
			expectVersion: "1.10.17-fh+hotfix (deadbeef) built-by-ci",
		},
		{
			name:          "unknown/future fork variant still maps to Ethereum block type",
			typeOrVariant: "optimism 1.0.0",
			expectType:    "sf.ethereum.type.v2.Block",
			expectVariant: "optimism",
			expectVersion: "1.0.0",
		},
		{
			name:          "single token that is not a valid FQN fails loudly",
			typeOrVariant: "geth",
			expectErr:     "invalid type",
		},
		{
			name:          "empty string fails loudly",
			typeOrVariant: "",
			expectErr:     "invalid type",
		},
		{
			name:          "type.googleapis.com prefixed FQN is still accepted",
			typeOrVariant: "type.googleapis.com/sf.cosmos.type.v2.Block",
			expectType:    "type.googleapis.com/sf.cosmos.type.v2.Block",
		},
		{
			name:          "FQN followed by a stray trailing token fails loudly instead of becoming Ethereum",
			typeOrVariant: "sf.cosmos.type.v2.Block extra",
			expectErr:     "invalid type",
		},
		{
			name:          "FQN followed by a trailing space fails loudly instead of becoming Ethereum",
			typeOrVariant: "sf.cosmos.type.v2.Block ",
			expectErr:     "invalid type",
		},
		{
			name:          "a leading stray space before a variant fails loudly instead of an empty variant",
			typeOrVariant: " geth 1.20.4",
			expectErr:     "invalid type",
		},
		{
			name:          "explicit FQN followed by node name and version is accepted, type is trusted as given",
			typeOrVariant: "sf.ethereum.type.v2.Block geth 1.20.4",
			expectType:    "sf.ethereum.type.v2.Block",
			expectVariant: "geth",
			expectVersion: "1.20.4",
		},
		{
			name:          "explicit FQN plus node name/version works for any chain, not just Ethereum",
			typeOrVariant: "sf.cosmos.type.v2.Block cosmos-node 1.2.3",
			expectType:    "sf.cosmos.type.v2.Block",
			expectVariant: "cosmos-node",
			expectVersion: "1.2.3",
		},
		{
			name:          "explicit FQN plus node name and a version string containing spaces is not truncated",
			typeOrVariant: "sf.ethereum.type.v2.Block geth 1.10.17-fh+hotfix (deadbeef)",
			expectType:    "sf.ethereum.type.v2.Block",
			expectVariant: "geth",
			expectVersion: "1.10.17-fh+hotfix (deadbeef)",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blockType, variant, version, err := resolveBlockType(test.typeOrVariant)
			if test.expectErr != "" {
				require.ErrorContains(t, err, test.expectErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, test.expectType, blockType)
			require.Equal(t, test.expectVariant, variant)
			require.Equal(t, test.expectVersion, version)
		})
	}
}

func Test_readInit_EthereumVariant(t *testing.T) {
	reader := &ConsoleReader{
		logger: zlogTest,
		tracer: tracerTest,
	}

	err := reader.readInit("3.0 geth 1.20.4")
	require.NoError(t, err)
	require.Equal(t, "type.googleapis.com/sf.ethereum.type.v2.Block", reader.protoMessageType)
}

func Test_readInit_ProtocolVersionStillValidated(t *testing.T) {
	reader := &ConsoleReader{
		logger: zlogTest,
		tracer: tracerTest,
	}

	err := reader.readInit("2.3 geth 1.9.0")
	require.ErrorContains(t, err, "unsupported")
}

func Test_GetNext_EthereumVariantInitLine(t *testing.T) {
	lines := make(chan string, 2)
	reader := newConsoleReader(lines, zlogTest, tracerTest)

	initLine := "FIRE INIT 3.0 geth 1.20.4"
	blockLine := "FIRE BLOCK 18571000 d2836a703a02f3ca2a13f05efe26fc48c6fa0db0d754a49e56b066d3b7d54659 18570999 55de88c909fa368ae1e93b6b8ffb3fbb12e64aefec1d4a1fcc27ae7633de2f81 18570800 1699992393935935000 Ci10eXBlLmdvb2dsZWFwaXMuY29tL3NmLmV0aGVyZXVtLnR5cGUudjIuQmxvY2sSJxIg0oNqcDoC88oqE/Be/ib8SMb6DbDXVKSeVrBm07fVRlkY+L3tCA=="

	lines <- initLine
	lines <- blockLine
	close(lines)

	block, err := reader.ReadBlock()
	require.NoError(t, err)

	require.Equal(t, uint64(18571000), block.Number)
	require.Equal(t, "type.googleapis.com/sf.ethereum.type.v2.Block", block.Payload.TypeUrl)
}

func Test_GetNext(t *testing.T) {
	lines := make(chan string, 2)
	reader := newConsoleReader(lines, zlogTest, tracerTest)

	initLine := "FIRE INIT 1.0 sf.ethereum.type.v2.Block"
	blockLine := "FIRE BLOCK 18571000 d2836a703a02f3ca2a13f05efe26fc48c6fa0db0d754a49e56b066d3b7d54659 18570999 55de88c909fa368ae1e93b6b8ffb3fbb12e64aefec1d4a1fcc27ae7633de2f81 18570800 1699992393935935000 Ci10eXBlLmdvb2dsZWFwaXMuY29tL3NmLmV0aGVyZXVtLnR5cGUudjIuQmxvY2sSJxIg0oNqcDoC88oqE/Be/ib8SMb6DbDXVKSeVrBm07fVRlkY+L3tCA=="

	lines <- initLine
	lines <- blockLine
	close(lines)

	block, err := reader.ReadBlock()
	require.NoError(t, err)

	require.Equal(t, uint64(18571000), block.Number)
	require.Equal(t, "d2836a703a02f3ca2a13f05efe26fc48c6fa0db0d754a49e56b066d3b7d54659", block.Id)
	require.Equal(t, "55de88c909fa368ae1e93b6b8ffb3fbb12e64aefec1d4a1fcc27ae7633de2f81", block.ParentId)
	require.Equal(t, uint64(18570800), block.LibNum)
	require.Equal(t, int32(time.Unix(0, 1699992393935935000).Nanosecond()), block.Timestamp.Nanos)
}
