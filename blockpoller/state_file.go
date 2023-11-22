package blockpoller

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/streamingfast/bstream"

	"github.com/streamingfast/bstream/forkable"
	"go.uber.org/zap"
)

type blockRef struct {
	Id          string `json:"id"`
	Num         uint64 `json:"num"`
	PrevBlockId string `json:"previous_ref_id"`
}

func (b blockRef) String() string {
	return fmt.Sprintf("%d (%s)", b.Num, b.Id)
}

func br(id string, num uint64, prevBlockId string) blockRef {
	return blockRef{
		Id:          id,
		Num:         num,
		PrevBlockId: prevBlockId,
	}
}

type stateFile struct {
	Lib            blockRef
	LastFiredBlock blockRef
	Blocks         []blockRef
}

func (p *BlockPoller) getState() (*stateFile, error) {
	if p.stateStorePath == "" {
		return nil, fmt.Errorf("no cursor store path set")
	}

	filepath := filepath.Join(p.stateStorePath, "cursor.json")
	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("unable to open cursor file %s: %w", filepath, err)
	}
	sf := stateFile{}
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&sf); err != nil {
		return nil, fmt.Errorf("feailed to decode cursor file %s: %w", filepath, err)
	}
	return &sf, nil
}

func (p *BlockPoller) saveState(blocks []*forkable.Block) error {
	if p.stateStorePath == "" {
		return nil
	}

	lastFiredBlock := blocks[len(blocks)-1]

	sf := stateFile{
		Lib:            br(p.forkDB.LIBID(), p.forkDB.LIBNum(), ""),
		LastFiredBlock: br(lastFiredBlock.BlockID, lastFiredBlock.BlockNum, lastFiredBlock.PreviousBlockID),
	}

	for _, blk := range blocks {
		sf.Blocks = append(sf.Blocks, br(blk.BlockID, blk.BlockNum, blk.PreviousBlockID))
	}

	filepath := filepath.Join(p.stateStorePath, "cursor.json")
	file, err := os.OpenFile(filepath, os.O_CREATE, os.ModePerm)
	if err != nil {
		return fmt.Errorf("unable to open cursor file %s: %w", filepath, err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(sf); err != nil {
		return fmt.Errorf("unable to encode cursor file %s: %w", filepath, err)
	}

	p.logger.Info("saved cursor",
		zap.Reflect("filepath", filepath),
		zap.Stringer("last_fired_block", sf.LastFiredBlock),
		zap.Stringer("lib", sf.Lib),
		zap.Int("block_count", len(sf.Blocks)),
	)
	return nil
}

func (p *BlockPoller) initState(resolvedStartBlock bstream.BlockRef) (*forkable.ForkDB, bstream.BlockRef, error) {
	forkDB := forkable.NewForkDB(forkable.ForkDBWithLogger(p.logger))

	sf, err := p.getState()
	if err != nil {
		p.logger.Warn("unable to load cursor file, initializing a new forkdb",
			zap.Stringer("start_block", resolvedStartBlock),
			zap.Stringer("lib", resolvedStartBlock),
			zap.Error(err),
		)
		forkDB.InitLIB(resolvedStartBlock)
		return forkDB, resolvedStartBlock, nil
	}

	forkDB.InitLIB(bstream.NewBlockRef(sf.Lib.Id, sf.Lib.Num))

	for _, blk := range sf.Blocks {
		b := &block{nil, true}
		forkDB.AddLink(bstream.NewBlockRef(blk.Id, blk.Num), blk.PrevBlockId, b)
	}

	p.logger.Info("loaded cursor",
		zap.Stringer("start_block", sf.LastFiredBlock),
		zap.Stringer("lib", sf.Lib),
		zap.Int("block_count", len(sf.Blocks)),
	)

	return forkDB, bstream.NewBlockRef(sf.LastFiredBlock.Id, sf.LastFiredBlock.Num), nil
}
