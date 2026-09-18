package firecore

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/firehose-core/node-manager/consoleline"
	logplugin "github.com/streamingfast/firehose-core/node-manager/log_plugin"
	"github.com/streamingfast/firehose-core/node-manager/superviser"
	"github.com/streamingfast/shutter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// linesPlugin forwards what the superviser reads to a console reader, as the mindreader
// plugin does.
type linesPlugin struct {
	*shutter.Shutter

	decodeBlocks bool
	blockLines   int
	lines        chan string
	consoleLines chan consoleline.Line
}

func (p *linesPlugin) Name() string          { return "linesPlugin" }
func (p *linesPlugin) Launch()               {}
func (p *linesPlugin) Stop()                 {}
func (p *linesPlugin) ReadsBlockLines() bool { return p.decodeBlocks }
func (p *linesPlugin) LogBlockLine(block *consoleline.Block) {
	p.blockLines++
	p.consoleLines <- consoleline.Line{Block: block}
}
func (p *linesPlugin) LogLine(line string) {
	if p.decodeBlocks {
		p.consoleLines <- consoleline.Line{Text: line}
		return
	}
	p.lines <- line
}

var _ logplugin.BlockLinePlugin = (*linesPlugin)(nil)

func TestConsoleReader_BlocksFromNodeProcess(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	payloads := [][]byte{make([]byte, 100), make([]byte, 40*1024*1024+7), nil, make([]byte, 3)}
	for _, payload := range payloads {
		r.Read(payload)
	}

	output := "FIRE INIT 3.1 sf.test.Block\nsome node log\r\n"
	for i, payload := range payloads {
		output += fmt.Sprintf("FIRE BLOCK %d %d h%d %d p%d %d 1700000000000000000 %s\n", 10+i, 1000+i, i, 9+i, i, 5, base64.StdEncoding.EncodeToString(payload))
	}

	outputFile := filepath.Join(t.TempDir(), "output")
	require.NoError(t, os.WriteFile(outputFile, []byte(output), 0o644))

	for _, decodeBlocks := range []bool{true, false} {
		t.Run(fmt.Sprintf("decode=%t", decodeBlocks), func(t *testing.T) {
			plugin := &linesPlugin{
				Shutter:      shutter.New(),
				decodeBlocks: decodeBlocks,
				lines:        make(chan string, 100),
				consoleLines: make(chan consoleline.Line, 100),
			}

			reader := newConsoleReader(plugin.lines, zlogTest, tracerTest)
			if decodeBlocks {
				reader.ReadLines(plugin.consoleLines)
			}

			process := superviser.New(zlogTest, "bash", []string{"-c", "cat " + outputFile})
			process.RegisterLogPlugin(plugin)
			require.NoError(t, process.Start())

			var blocks []*pbbstream.Block
			done := make(chan error)
			go func() {
				for {
					block, err := reader.ReadBlock()
					if err != nil {
						done <- err
						return
					}
					blocks = append(blocks, block)

					if len(blocks) == len(payloads) {
						done <- nil
						return
					}
				}
			}()

			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(30 * time.Second):
				t.Fatal("timeout waiting for blocks")
			}

			require.Len(t, blocks, len(payloads))
			if decodeBlocks {
				assert.Equal(t, len(payloads), plugin.blockLines)
			} else {
				assert.Equal(t, 0, plugin.blockLines)
			}
			for i, block := range blocks {
				assert.Equal(t, uint64(10+i), block.Number)
				assert.Equal(t, int32(i), block.PartialIndex)
				assert.True(t, block.LastPartial)
				assert.Equal(t, fmt.Sprintf("h%d", i), block.Id)
				assert.Equal(t, "type.googleapis.com/sf.test.Block", block.Payload.TypeUrl)
				assert.True(t, string(payloads[i]) == string(block.Payload.Value), "block %d payload differs", i)
			}
		})
	}
}

func TestConsoleReader_DecodedBlockErrors(t *testing.T) {
	reader := newConsoleReader(nil, zlogTest, tracerTest)

	_, err := reader.readDecodedBlock(&consoleline.Block{Header: "1 h1 0 p0 0 1700000000"})
	require.ErrorContains(t, err, "reader protocol version not set")

	require.NoError(t, reader.readInit("3.0 sf.test.Block"))

	_, err = reader.readDecodedBlock(&consoleline.Block{Header: "1 h1 0 p0 0"})
	require.ErrorContains(t, err, "splitting block log line")

	_, err = reader.readDecodedBlock(&consoleline.Block{Header: "x h1 0 p0 0 1700000000"})
	require.ErrorContains(t, err, "parsing block num")

	_, err = reader.readDecodedBlock(&consoleline.Block{Header: "1 h1 0 p0 0 1700000000", Err: fmt.Errorf("decoding payload: bad")})
	require.ErrorContains(t, err, "decoding payload: bad")

	block, err := reader.readDecodedBlock(&consoleline.Block{Header: "1 h1 0 p0 0 1700000000", Payload: []byte("abc")})
	require.NoError(t, err)
	assert.Equal(t, []byte("abc"), block.Payload.Value)
}
