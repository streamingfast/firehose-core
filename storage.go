package firecore

import (
	"fmt"

	"github.com/spf13/viper"
	"github.com/streamingfast/dstore"
)

var commonStoresCreated bool
var indexStoreCreated bool

func GetCommonStoresURLs(dataDir string) (mergedBlocksStoreURL, oneBlocksStoreURL, forkedBlocksStoreURL string, err error) {
	mergedBlocksStoreURL = MustReplaceDataDir(dataDir, viper.GetString("common-merged-blocks-store-url"))
	oneBlocksStoreURL = MustReplaceDataDir(dataDir, viper.GetString("common-one-block-store-url"))
	forkedBlocksStoreURL = MustReplaceDataDir(dataDir, viper.GetString("common-forked-blocks-store-url"))

	if commonStoresCreated {
		return
	}

	if err = mkdirStorePathIfLocal(forkedBlocksStoreURL); err != nil {
		return
	}

	if err = mkdirStorePathIfLocal(oneBlocksStoreURL); err != nil {
		return
	}

	if err = mkdirStorePathIfLocal(mergedBlocksStoreURL); err != nil {
		return
	}

	commonStoresCreated = true
	return
}

func GetIndexStore(dataDir string) (indexStore dstore.Store, possibleIndexSizes []uint64, err error) {
	indexStoreURL := MustReplaceDataDir(dataDir, viper.GetString("common-index-store-url"))

	if indexStoreURL != "" {
		s, err := dstore.NewStore(indexStoreURL, "", "", false)
		if err != nil {
			return nil, nil, fmt.Errorf("couldn't create index store: %w", err)
		}
		if !indexStoreCreated {
			if err = mkdirStorePathIfLocal(indexStoreURL); err != nil {
				return nil, nil, err
			}
		}
		indexStoreCreated = true
		indexStore = s
	}

	for _, size := range viper.GetIntSlice("common-index-block-sizes") {
		if size < 0 {
			return nil, nil, fmt.Errorf("invalid negative size for common-index-block-sizes: %d", size)
		}
		possibleIndexSizes = append(possibleIndexSizes, uint64(size))
	}

	return
}
