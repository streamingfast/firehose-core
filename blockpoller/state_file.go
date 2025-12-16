package blockpoller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/forkable"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/firehose-core/rpc"
	"go.uber.org/zap"
)

type blockRef struct {
	Id  string `json:"id"`
	Num uint64 `json:"num"`
}

type blockRefWithPrev struct {
	blockRef
	PrevBlockId string `json:"previous_ref_id"`
}

func (b blockRef) String() string {
	return fmt.Sprintf("%d (%s)", b.Num, b.Id)
}

type stateFile struct {
	Lib            blockRef
	LastFiredBlock blockRefWithPrev
	Blocks         []blockRefWithPrev
}

func (p *BlockPoller[C]) isStateFileExist(stateStorePath string) bool {
	if stateStorePath == "" {
		p.logger.Info("No state store path set, skipping cursor check")
		return false
	}
	fp := filepath.Join(stateStorePath, "cursor.json")
	_, err := os.Stat(fp)
	exist := err == nil
	p.logger.Info("cursor file check",
		zap.String("state_store_path", stateStorePath),
		zap.Bool("exist", exist),
	)
	return exist
}

func getState(stateStorePath string) (*stateFile, error) {
	if stateStorePath == "" {
		return nil, fmt.Errorf("no cursor store path set")
	}

	fp := filepath.Join(stateStorePath, "cursor.json")
	file, err := os.Open(fp)
	if err != nil {
		return nil, fmt.Errorf("unable to open cursor file %s: %w", fp, err)
	}
	sf := stateFile{}
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&sf); err != nil {
		return nil, fmt.Errorf("feailed to decode cursor file %s: %w", fp, err)
	}
	return &sf, nil
}

func (p *BlockPoller[C]) saveState(blocks []*forkable.Block) error {
	p.logger.Debug("saving cursor", zap.String("state_store_path", p.stateStorePath))
	if p.stateStorePath == "" {
		return nil
	}

	if len(blocks) == 0 {
		p.logger.Warn("skipping saving state because of an empty block list")
		return nil
	}

	lastFiredBlock := blocks[len(blocks)-1]

	sf := stateFile{
		Lib:            blockRef{p.forkDB.LIBID(), p.forkDB.LIBNum()},
		LastFiredBlock: blockRefWithPrev{blockRef{lastFiredBlock.BlockID, lastFiredBlock.BlockNum}, lastFiredBlock.PreviousBlockID},
	}

	for _, blk := range blocks {
		sf.Blocks = append(sf.Blocks, blockRefWithPrev{blockRef{blk.BlockID, blk.BlockNum}, blk.PreviousBlockID})
	}

	cnt, err := json.Marshal(sf)
	if err != nil {
		return fmt.Errorf("unable to marshal stateFile: %w", err)
	}

	err = os.MkdirAll(p.stateStorePath, os.ModePerm)
	if err != nil {
		return fmt.Errorf("making state store path: %w", err)
	}
	fpath := filepath.Join(p.stateStorePath, "cursor.json")

	if err := os.WriteFile(fpath, cnt, 0666); err != nil {
		return fmt.Errorf("unable to open cursor file %s: %w", fpath, err)
	}

	p.logger.Info("saved cursor",
		zap.Reflect("filepath", fpath),
		zap.Stringer("last_fired_block", sf.LastFiredBlock),
		zap.Stringer("lib", sf.Lib),
		zap.Int("block_count", len(sf.Blocks)),
		zap.Bool("keep", false),
	)
	return nil
}

func (p *BlockPoller[C]) initState(firstStreamableBlockNum uint64, stateStorePath string, ignoreCursor bool, logger *zap.Logger) (*forkable.ForkDB, bstream.BlockRef, error) {
	forkDB := forkable.NewForkDB(forkable.ForkDBWithLogger(logger))

	if ignoreCursor || !p.isStateFileExist(stateStorePath) {
		logger.Info("ignoring cursor, fetching first streamable block", zap.Uint64("first_streamable_block", firstStreamableBlockNum))

		for {
			br, err := rpc.WithClients(p.clients, func(ctx context.Context, client C) (*FetchResponse, error) {
				firstStreamableBlock, skip, err := p.blockFetcher.Fetch(ctx, client, firstStreamableBlockNum)
				if err != nil {
					return nil, fmt.Errorf("fetching first streamable block: %w", err)
				}
				if skip {
					return nil, fmt.Errorf("expecting first streamable block %q not to be skipped", firstStreamableBlock.AsRef())
				}
				return &FetchResponse{
					Block: firstStreamableBlock,
				}, nil
			})
			if err != nil {
				p.logger.Warn("fetching first streamable block", zap.Uint64("first_streamable_block", firstStreamableBlockNum), zap.Error(err))
				continue
			}
			if br.Skipped {
				return nil, nil, fmt.Errorf("expecting first streamable block %q not to be skiped", firstStreamableBlockNum)
			}

			firstStreamableBlockRef := br.Block.AsRef()

			logger.Info("ignoring cursor, will start from...",
				zap.Stringer("first_streamable_block", firstStreamableBlockRef),
				zap.Stringer("lib", firstStreamableBlockRef),
			)
			forkDB.InitLIB(firstStreamableBlockRef)

			return forkDB, firstStreamableBlockRef, nil
		}
	}

	sf, err := getState(stateStorePath) //at this point we expect the stateFile to exist ...
	if err != nil {
		return nil, nil, fmt.Errorf("loading cursor: %w", err)
	}

	forkDB.InitLIB(bstream.NewBlockRef(sf.Lib.Id, sf.Lib.Num))

	for _, blk := range sf.Blocks {
		b := &block{
			Block: &pbbstream.Block{
				Number:   blk.Num,
				Id:       blk.Id,
				ParentId: blk.PrevBlockId,
			},
			fired: true,
		}
		forkDB.AddLink(bstream.NewBlockRef(blk.Id, blk.Num), blk.PrevBlockId, b)
	}

	logger.Info("loaded cursor",
		zap.Stringer("start_block", sf.LastFiredBlock),
		zap.Stringer("lib", sf.Lib),
		zap.Int("block_count", len(sf.Blocks)),
	)

	return forkDB, bstream.NewBlockRef(sf.LastFiredBlock.Id, sf.LastFiredBlock.Num), nil
}
