// Copyright 2021 dfuse Platform Inc.
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

package compare

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/diffx"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/cmd/tools/check"
	fcjson "github.com/streamingfast/firehose-core/json"
	fcproto "github.com/streamingfast/firehose-core/proto"
	"github.com/streamingfast/firehose-core/types"
	"go.uber.org/multierr"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
)

type BlockDifferences struct {
	BlockNumber uint64
	Differences []string
}

func NewToolsCompareBlocksCmd[B firecore.Block](chain *firecore.Chain[B], zlog *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compare-blocks <reference_blocks_store> <current_blocks_store> [<block_range>]",
		Short: "Checks for any differences between two block stores between a specified range. (To compare the likeness of two block ranges, for example)",
		Long: cli.Dedent(`
			The 'compare-blocks' takes in two paths to stores of either merged blocks or one-blocks and a range
			specifying the blocks you want to compare, written as: '<start>:<finish>'. It will output the status
			of the likeness of every 100,000 blocks, on completion, or on encountering a difference. Increments that
			contain a difference will be communicated as well as the blocks within that contain differences. Increments
			that do not have any differences will be outputted as identical.

			After passing through the blocks, it will output instructions on how to locate a specific difference
			based on the blocks that were given. This is done by applying the '--diff' flag before your args.

			The --diff flag controls how differences are displayed:
			  --diff or --diff=inline  Print inline diffs using diffx (ANSI color, line numbers, character-level highlighting)
			  --diff=editor            Open each differing block in $DIFF_EDITOR; falls back to 'diff -u' if not set
			  --diff=<cmd>             Treat the value as an editor command (e.g. 'vimdiff', 'code --wait --diff')
		`),
		Args: cobra.ExactArgs(3),
		RunE: runCompareBlocksE(chain, zlog),
		Example: firecore.ExamplePrefixed(chain, "tools compare-blocks", `
			# Compare a single block (auto-expands to its 100-block bundle)
			reference_store/ current_store/ 2713

			# Run over full block range
			reference_store/ current_store/ 0:16000000

			# Run over specific block range, displaying inline differences
			--diff reference_store/ current_store/ 100:200

			# Run over specific block range, opening differences in $DIFF_EDITOR (or 'diff -u' fallback)
			--diff=editor reference_store/ current_store/ 100:200

			# Run over specific block range, opening differences in vimdiff
			--diff=vimdiff reference_store/ current_store/ 100:200
		`),
	}

	flags := cmd.PersistentFlags()
	flags.String("diff", "", cli.FlagDescription(`
		Show diff for each differing block. Accepts an optional value:
		  (no value) or 'inline'  Print inline diffs using diffx
		  'editor'                Open $DIFF_EDITOR, falling back to 'diff -u' if unset
		  <command>               Use the given command as the diff editor (e.g. 'vimdiff', 'code --wait --diff')
	`))
	cmd.Flag("diff").NoOptDefVal = "inline"
	flags.Bool("include-unknown-fields", false, "When activated, the 'unknown fields' in the protobuf message will also be compared. These would not generate any difference when unmarshalled with the current protobuf definition.")

	return cmd
}

func runCompareBlocksE[B firecore.Block](chain *firecore.Chain[B], zlog *zap.Logger) firecore.CommandExecutor {

	return func(cmd *cobra.Command, args []string) error {
		diffMode := sflags.MustGetString(cmd, "diff")
		includeUnknownFields := sflags.MustGetBool(cmd, "include-unknown-fields")
		protoPaths := sflags.MustGetStringSlice(cmd, "proto-paths")
		bytesEncoding := sflags.MustGetString(cmd, "bytes-encoding")
		segmentSize := uint64(100000)
		warnAboutExtraBlocks := sync.Once{}
		ctx := cmd.Context()
		blockRange, err := types.GetBlockRangeFromArg(args[2])
		if err != nil {
			return fmt.Errorf("parsing range: %w", err)
		}

		bundleSize, err := firecore.GetMergedBlocksBundleSizeFlag(cmd)
		if err != nil {
			return err
		}
		if !strings.Contains(args[2], ":") && blockRange.IsOpen() && blockRange.Start >= 0 {
			n := uint64(blockRange.Start)
			blockRange = types.NewClosedRange(int64(n), n+1)
			zlog.Debug("single block argument, comparing only that block", zap.Uint64("block", n))
		}

		if !blockRange.IsResolved() {
			return fmt.Errorf("invalid block range, you must provide a closed range fully resolved (no negative value)")
		}

		stopBlock := blockRange.GetStopBlockOr(firecore.MaxUint64)

		// Create stores
		storeReference, err := dstore.NewDBinStore(args[0])
		if err != nil {
			return fmt.Errorf("unable to create store at path %q: %w", args[0], err)
		}
		storeCurrent, err := dstore.NewDBinStore(args[1])
		if err != nil {
			return fmt.Errorf("unable to create store at path %q: %w", args[1], err)
		}

		segments, err := blockRange.Split(segmentSize, types.EndBoundaryExclusive)
		if err != nil {
			return fmt.Errorf("unable to split blockrage in segments: %w", err)
		}
		processState := &state{
			segments: segments,
		}

		registry, err := fcproto.NewRegistry(nil, protoPaths...)
		if err != nil {
			return fmt.Errorf("creating registry: %w", err)
		}

		sanitizer := chain.Tools.GetSanitizeBlockForCompare()

		err = storeReference.Walk(ctx, check.WalkBlockPrefix(blockRange, bundleSize), func(filename string) (err error) {
			var isOneBlock bool
			fileStartBlock, err := strconv.ParseUint(filename, 10, 64)
			if err != nil {
				fileStartBlock, _, _, _, _, err = bstream.ParseFilename(filename)
				if err != nil {
					return fmt.Errorf("parsing filename: %w", err)
				}
				isOneBlock = true
			}

			// If reached end of range
			if stopBlock <= uint64(fileStartBlock) {
				return dstore.StopIteration
			}

			// For one-block files the filename IS the block number so a direct Contains is correct.
			// For merged-block files the filename is the bundle start, so we need an overlap test:
			// the bundle covers [fileStartBlock, fileStartBlock+bundleSize) and we want any
			// intersection with [blockRange.Start, stopBlock).
			inRange := isOneBlock &&
				blockRange.Contains(uint64(fileStartBlock), types.EndBoundaryExclusive) ||
				!isOneBlock &&
					fileStartBlock < stopBlock && uint64(blockRange.Start) < fileStartBlock+bundleSize

			if inRange {
				var wg sync.WaitGroup
				var bundleErrLock sync.Mutex
				var bundleReadErr error
				var referenceBlockHashes []string
				var referenceBlocks map[string]*dynamicpb.Message
				var referenceBlocksNum map[string]uint64
				var currentBlocks map[string]*dynamicpb.Message

				rightSideFilename := filename

				exists, err := storeCurrent.FileExists(ctx, filename)
				if err != nil {
					return fmt.Errorf("checking file %q exists in current store: %w", filename, err)
				}
				if !exists {
					if isOneBlock {
						prefix := fmt.Sprintf("%010d-", fileStartBlock)
						matchingFiles, err := storeCurrent.ListFiles(ctx, prefix, 1)
						if err != nil {
							return fmt.Errorf("listing files with prefix %q", prefix)
						}
						if len(matchingFiles) == 0 {
							fmt.Printf("Bundle file %s does not exist in current store, skipping\n", filename)
							return nil
						}
						rightSideFilename = matchingFiles[0]
					}
				}

				wg.Go(func() {
					referenceBlockHashes, referenceBlocks, referenceBlocksNum, err = readBundle(
						ctx,
						filename,
						storeReference,
						uint64(fileStartBlock),
						stopBlock,
						&warnAboutExtraBlocks,
						sanitizer,
						registry,
					)
					if err != nil {
						bundleErrLock.Lock()
						bundleReadErr = multierr.Append(bundleReadErr, err)
						bundleErrLock.Unlock()
					}
				})

				wg.Go(func() {
					_, currentBlocks, _, err = readBundle(ctx,
						rightSideFilename,
						storeCurrent,
						uint64(fileStartBlock),
						stopBlock,
						&warnAboutExtraBlocks,
						sanitizer,
						registry,
					)
					if err != nil {
						bundleErrLock.Lock()
						bundleReadErr = multierr.Append(bundleReadErr, err)
						bundleErrLock.Unlock()
					}
				})
				wg.Wait()
				if bundleReadErr != nil {
					return fmt.Errorf("reading bundles: %w", bundleReadErr)
				}

				outLock := sync.Mutex{}
				for _, referenceBlockHash := range referenceBlockHashes {
					wg.Go(func() {
						referenceBlock := referenceBlocks[referenceBlockHash]
						currentBlock, existsInCurrent := currentBlocks[referenceBlockHash]
						referenceBlockNum := referenceBlocksNum[referenceBlockHash]

						// Skip blocks that precede the range start. This happens when a merged-block
						// file starts before blockRange.Start (e.g. single-block input mid-bundle).
						if referenceBlockNum < uint64(blockRange.Start) {
							return
						}

						var isDifferent bool
						if existsInCurrent {
							refJSON, curJSON, different, compareErr := Compare(referenceBlock, currentBlock, includeUnknownFields, registry, bytesEncoding)

							isDifferent = different

							if isDifferent {
								outLock.Lock()
								shortHash := referenceBlockHash
								if len(shortHash) > 8 {
									shortHash = shortHash[:8] + "..."
								}
								fmt.Printf("- Block %d (%s) is different\n", referenceBlockNum, shortHash)
								switch diffMode {
								case "inline":
									if writeErr := diffx.WriteJSONDiff(os.Stdout, refJSON, curJSON); writeErr != nil && compareErr == nil {
										compareErr = writeErr
									}
								case "editor":
									editorCmd := cmp.Or(os.Getenv("DIFF_EDITOR"), "diff -u")
									if openErr := openDiffEditor(editorCmd, refJSON, curJSON, referenceBlockNum); openErr != nil {
										fmt.Printf("  ! failed to open diff editor: %s\n", openErr)
									}
								case "":
									// no diff displayed
								default:
									if openErr := openDiffEditor(diffMode, refJSON, curJSON, referenceBlockNum); openErr != nil {
										fmt.Printf("  ! failed to open diff editor: %s\n", openErr)
									}
								}
								if compareErr != nil {
									fmt.Printf("  ! diff error: %s\n", compareErr)
								}
								outLock.Unlock()
							}
						}
						processState.process(referenceBlockNum, isDifferent, !existsInCurrent)
					})
					wg.Wait()
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("walking files: %w", err)
		}
		processState.print()

		if processState.totalDifferencesFound > 0 {
			fmt.Println()
			fmt.Println("Re-run with --diff to see inline differences, or --diff=editor to open $DIFF_EDITOR")
		}

		return nil
	}
}

// openDiffEditor writes both JSON representations to temp files and opens the
// configured diff editor with them, waiting for it to exit before returning.
// The temp files are removed after the editor exits.
func openDiffEditor(editorCmd, refJSON, curJSON string, blockNum uint64) error {
	refFile, err := os.CreateTemp("", fmt.Sprintf("block_%d_reference_*.json", blockNum))
	if err != nil {
		return fmt.Errorf("creating reference temp file: %w", err)
	}

	curFile, err := os.CreateTemp("", fmt.Sprintf("block_%d_current_*.json", blockNum))
	if err != nil {
		refFile.Close()
		return fmt.Errorf("creating current temp file: %w", err)
	}

	if _, err := refFile.WriteString(refJSON); err != nil {
		refFile.Close()
		curFile.Close()
		return fmt.Errorf("writing reference temp file: %w", err)
	}
	refFile.Close()

	if _, err := curFile.WriteString(curJSON); err != nil {
		curFile.Close()
		return fmt.Errorf("writing current temp file: %w", err)
	}
	curFile.Close()

	parts := strings.Fields(editorCmd)
	cmdArgs := append(parts[1:], refFile.Name(), curFile.Name())
	c := exec.Command(parts[0], cmdArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	if err := c.Run(); err != nil {
		// Exit code 1 from diff tools (e.g. 'diff -u') means differences were found — not an error.
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.ExitCode() == 1 {
			return nil
		}
		return err
	}
	return nil
}

func readBundle(
	ctx context.Context,
	filename string,
	store dstore.Store,
	fileStartBlock,
	stopBlock uint64,
	warnAboutExtraBlocks *sync.Once,
	sanitizer firecore.SanitizeBlockForCompareFunc,
	registry *fcproto.Registry,
) ([]string, map[string]*dynamicpb.Message, map[string]uint64, error) {
	fileReader, err := store.OpenObject(ctx, filename)

	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating reader for filename %q: %w", filename, err)
	}

	blockReader, err := bstream.NewDBinBlockReader(fileReader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating block reader: %w", err)
	}

	var blockHashes []string
	blocksMap := make(map[string]*dynamicpb.Message)
	blockNumMap := make(map[string]uint64)
	for {
		curBlock, err := blockReader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, nil, fmt.Errorf("reading blocks: %w", err)
		}
		if curBlock.Number >= stopBlock {
			break
		}
		if curBlock.Number < fileStartBlock {
			warnAboutExtraBlocks.Do(func() {
				fmt.Printf("Warn: Bundle file %s contains block %d, preceding its start_block. This 'feature' is not used anymore and extra blocks like this one will be ignored during compare\n", store.ObjectURL(filename), curBlock.Number)
			})
			continue
		}

		if sanitizer != nil {
			curBlock = sanitizer(curBlock)
		}

		curBlockPB, err := registry.Unmarshal(curBlock.Payload)

		if err != nil {
			return nil, nil, nil, fmt.Errorf("unmarshalling block: %w", err)
		}
		blockHashes = append(blockHashes, curBlock.Id)
		blockNumMap[curBlock.Id] = curBlock.Number
		blocksMap[curBlock.Id] = curBlockPB
	}

	return blockHashes, blocksMap, blockNumMap, nil
}

type state struct {
	segments                   []types.BlockRange
	currentSegmentIdx          int
	blocksCountedInThisSegment int
	differencesFound           int
	missingBlocks              int

	totalBlocksCounted    int
	totalDifferencesFound int
}

func (s *state) process(blockNum uint64, isDifferent bool, isMissing bool) {
	if !s.segments[s.currentSegmentIdx].Contains(blockNum, types.EndBoundaryExclusive) { // moving forward
		s.print()
		for i := s.currentSegmentIdx; i < len(s.segments); i++ {
			if s.segments[i].Contains(blockNum, types.EndBoundaryExclusive) {
				s.currentSegmentIdx = i
				s.totalBlocksCounted += s.blocksCountedInThisSegment
				s.differencesFound = 0
				s.missingBlocks = 0
				s.blocksCountedInThisSegment = 0
			}
		}
	}

	s.totalBlocksCounted++
	if isMissing {
		s.missingBlocks++
	} else if isDifferent {
		s.differencesFound++
		s.totalDifferencesFound++
	}
}

func (s *state) print() {
	seg := s.segments[s.currentSegmentIdx]
	rawStop := seg.GetStopBlockOr(firecore.MaxUint64)
	var endBlock string
	if rawStop == firecore.MaxUint64 {
		endBlock = "∞"
	} else {
		endBlock = fmt.Sprintf("%d", rawStop-1)
	}

	if s.totalBlocksCounted == 0 {
		fmt.Printf("✖ No blocks were found at all for segment %d - %s\n", seg.Start, endBlock)
		return
	}

	if s.differencesFound == 0 && s.missingBlocks == 0 {
		fmt.Printf("✓ Segment %d - %s has no differences (%d blocks counted)\n", seg.Start, endBlock, s.totalBlocksCounted)
		return
	}

	if s.differencesFound == 0 && s.missingBlocks != 0 {
		fmt.Printf("✓~ Segment %d - %s has no differences but does have %d missing blocks (%d blocks counted)\n", seg.Start, endBlock, s.missingBlocks, s.totalBlocksCounted)
		return
	}

	fmt.Printf("✖ Segment %d - %s has %d different blocks and %d missing blocks (%d blocks counted)\n", seg.Start, endBlock, s.differencesFound, s.missingBlocks, s.totalBlocksCounted)
}

// Compare marshals both proto messages to JSON and returns their representations
// along with whether they differ. The returned JSON strings are normalized
// (sorted keys, consistent indentation) for use with diffx.WriteJSONDiff.
func Compare(reference proto.Message, current proto.Message, includeUnknownFields bool, registry *fcproto.Registry, bytesEncoding string) (referenceJSON string, currentJSON string, isDifferent bool, err error) {
	if reference == nil && current == nil {
		return "", "", false, nil
	}
	if reflect.TypeOf(reference).Kind() == reflect.Ptr && reference == current {
		return "", "", false, nil
	}

	referenceMsg := reference.ProtoReflect()
	currentMsg := current.ProtoReflect()
	if referenceMsg.IsValid() && !currentMsg.IsValid() {
		return "", "", true, nil
	}
	if !referenceMsg.IsValid() && currentMsg.IsValid() {
		return "", "", true, nil
	}

	if proto.Equal(reference, current) {
		return "", "", false, nil
	}

	var opts []fcjson.MarshallerOption
	if !includeUnknownFields {
		opts = append(opts, fcjson.WithoutUnknownFields())
	}
	if bytesEncoding != "" {
		opts = append(opts, fcjson.WithBytesEncoding(bytesEncoding))
	}

	encoder := fcjson.NewMarshaller(registry, opts...)

	referenceJSON, err = encoder.MarshalToString(reference, jsontext.WithIndent("  "))
	cli.NoError(err, "marshal JSON reference")

	currentJSON, err = encoder.MarshalToString(current, jsontext.WithIndent("  "))
	cli.NoError(err, "marshal JSON current")

	return referenceJSON, currentJSON, true, nil
}
