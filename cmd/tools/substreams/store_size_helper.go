package substreams

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/streamingfast/logging"
	"go.uber.org/zap"
	"google.golang.org/api/iterator"
)

var zlog, _ = logging.PackageLogger("substreams-store-size", "github.com/streamingfast/firehose-core/cmd/tools/substreams")

// StoreSizes contains the compressed (total) and uncompressed (live) sizes
type StoreSizes struct {
	TotalCompressed  int64  // Total size of all files (compressed)
	LiveUncompressed *int64 // Uncompressed size from latest file metadata (nil if not available)
	LiveCompressed   int64  // Compressed size of latest file only
	LatestFile       string // Name of the latest file
}

// StoreSizeQuerier provides an interface for getting the total size of files
// in a storage location. This is structured to be easily portable back to dstore.
type StoreSizeQuerier interface {
	// GetStoreSizes returns both total compressed size and latest file's uncompressed size
	GetStoreSizes(ctx context.Context, path string) (*StoreSizes, error)
}

// NewStoreSizeQuerier creates an appropriate size querier based on the storage URL
func NewStoreSizeQuerier(storeURL string) (StoreSizeQuerier, error) {
	if strings.HasPrefix(storeURL, "gs://") {
		return &gcsStoreSizeQuerier{baseURL: storeURL}, nil
	}

	// Treat s3:// and az:// as unsupported for now
	if strings.HasPrefix(storeURL, "s3://") || strings.HasPrefix(storeURL, "az://") {
		return nil, fmt.Errorf("storage type not supported yet, only 'file://' and 'gs://' are supported")
	}

	// Everything else is treated as a local file path
	// Remove file:// prefix if present
	localPath := strings.TrimPrefix(storeURL, "file://")
	return &localStoreSizeQuerier{basePath: localPath}, nil
}

// localStoreSizeQuerier implements size queries for local filesystem storage
type localStoreSizeQuerier struct {
	basePath string
}

func (q *localStoreSizeQuerier) GetStoreSizes(ctx context.Context, path string) (*StoreSizes, error) {
	fullPath := filepath.Join(q.basePath, path)

	// Check if path exists
	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &StoreSizes{}, nil // Empty store
		}
		return nil, fmt.Errorf("stat %q: %w", fullPath, err)
	}

	// If it's a single file, return its size
	if !info.IsDir() {
		return &StoreSizes{
			TotalCompressed: info.Size(),
			LiveCompressed:  info.Size(),
			LatestFile:      filepath.Base(fullPath),
		}, nil
	}

	// Walk the directory tree and collect all files
	type fileEntry struct {
		path string
		size int64
	}
	var files []fileEntry
	var totalSize int64

	err = filepath.WalkDir(fullPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Check for context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return fmt.Errorf("getting file info for %q: %w", path, err)
			}
			files = append(files, fileEntry{path: path, size: info.Size()})
			totalSize += info.Size()
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("walking directory %q: %w", fullPath, err)
	}

	if len(files) == 0 {
		return &StoreSizes{}, nil
	}

	// Sort files by name (lexicographically) to get the latest
	sort.Slice(files, func(i, j int) bool {
		return files[i].path > files[j].path
	})

	latestFile := files[0]

	return &StoreSizes{
		TotalCompressed: totalSize,
		LiveCompressed:  latestFile.size,
		LatestFile:      filepath.Base(latestFile.path),
		// Note: Local files don't have metadata, so LiveUncompressed stays nil
	}, nil
}

// gcsStoreSizeQuerier implements size queries for Google Cloud Storage
type gcsStoreSizeQuerier struct {
	baseURL string
}

func (q *gcsStoreSizeQuerier) GetStoreSizes(ctx context.Context, path string) (*StoreSizes, error) {
	// Parse GCS URL: gs://bucket/prefix
	withoutScheme := strings.TrimPrefix(q.baseURL, "gs://")
	parts := strings.SplitN(withoutScheme, "/", 2)
	if len(parts) == 0 {
		return nil, fmt.Errorf("invalid GCS URL: %q", q.baseURL)
	}

	bucketName := parts[0]
	var basePrefix string
	if len(parts) > 1 {
		basePrefix = parts[1]
	}

	// Combine base prefix with requested path
	fullPrefix := filepath.Join(basePrefix, path)
	// GCS uses forward slashes, filepath.Join might use backslashes on Windows
	fullPrefix = strings.ReplaceAll(fullPrefix, "\\", "/")

	// Ensure prefix ends with / if it doesn't, to avoid matching partial names
	// unless it's empty
	if fullPrefix != "" && !strings.HasSuffix(fullPrefix, "/") {
		fullPrefix = fullPrefix + "/"
	}

	zlog.Debug("querying GCS store sizes",
		zap.String("bucket", bucketName),
		zap.String("base_prefix", basePrefix),
		zap.String("path", path),
		zap.String("full_prefix", fullPrefix),
	)

	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("creating GCS client: %w", err)
	}
	defer client.Close()

	bucket := client.Bucket(bucketName)

	// True binary search: Find the exact boundary where files exist.
	// Files are named like: states/0074981000-0000000000.kv or states/0074981000-0000000000.kv.zst
	// (dstore may add .zst compression transparently).
	// State files are always aligned to 1000-block boundaries.
	statesPrefix := fullPrefix + "states/"
	zlog.Debug("using states prefix", zap.String("states_prefix", statesPrefix))

	// isStateFile returns true for .kv and .kv.zst files (dstore may add .zst transparently).
	isStateFile := func(name string) bool {
		return strings.HasSuffix(name, ".kv") || strings.HasSuffix(name, ".kv.zst")
	}

	// Helper to check if any state files exist at or above a block number
	hasFilesAtOrAbove := func(blockNum uint64) (bool, error) {
		blockPrefix := fmt.Sprintf("%010d", blockNum)
		startOffset := statesPrefix + blockPrefix

		query := &storage.Query{
			Prefix:      statesPrefix,
			StartOffset: startOffset,
			Projection:  storage.ProjectionNoACL,
		}

		it := bucket.Objects(ctx, query)
		attrs, err := it.Next()
		if err == iterator.Done {
			return false, nil
		}
		if err != nil {
			return false, err
		}

		return isStateFile(attrs.Name), nil
	}

	// Phase 1: Find initial range using exponential probes
	probePoints := []uint64{
		100_000_000, 50_000_000, 25_000_000, 10_000_000,
		5_000_000, 1_000_000, 500_000, 100_000, 50_000,
		25_000, 10_000, 5_000, 1_000, 0,
	}

	var low, high uint64
	foundRange := false

	for i, probe := range probePoints {
		hasFiles, err := hasFilesAtOrAbove(probe)
		if err != nil {
			return nil, fmt.Errorf("probing block %d: %w", probe, err)
		}

		zlog.Debug("probe result", zap.Uint64("block", probe), zap.Bool("has_files", hasFiles))

		if hasFiles {
			// Found files at this probe
			// The actual latest block is between this probe and the previous (higher) probe
			if i == 0 {
				// Files exist at highest probe, set high to a very large value
				low = probe
				high = 1_000_000_000 // 1 billion
			} else {
				low = probe
				high = probePoints[i-1]
			}
			foundRange = true
			zlog.Debug("initial range found", zap.Uint64("low", low), zap.Uint64("high", high))
			break
		}
	}

	if !foundRange {
		zlog.Debug("no files found in any probe range")
		return &StoreSizes{}, nil
	}

	// Phase 2: Binary search to narrow down to 1000-block range
	// State files are aligned to 1000 blocks, so we search in 1000-block increments
	const blockAlignment = 1000

	for high-low > blockAlignment {
		mid := low + (high-low)/2
		// Round down to nearest 1000-block boundary
		mid = (mid / blockAlignment) * blockAlignment

		hasFiles, err := hasFilesAtOrAbove(mid)
		if err != nil {
			return nil, fmt.Errorf("binary search at block %d: %w", mid, err)
		}

		zlog.Debug("binary search iteration",
			zap.Uint64("low", low),
			zap.Uint64("mid", mid),
			zap.Uint64("high", high),
			zap.Bool("has_files", hasFiles),
		)

		if hasFiles {
			// Files exist at mid or above, search higher
			low = mid
		} else {
			// No files at mid or above, search lower
			high = mid
		}
	}

	zlog.Debug("binary search complete", zap.Uint64("final_low", low), zap.Uint64("final_high", high))

	// Phase 3: List files starting from the binary search result
	// 'low' contains the highest 1000-block boundary where files exist
	// We need to list backwards from there to find files with metadata
	// Start from a block range before 'low' to ensure we get files with metadata
	const blockLookback = 20000 // Look back 20k blocks to find files with metadata
	startBlock := low
	if low > blockLookback {
		startBlock = low - blockLookback
	} else {
		startBlock = 0
	}

	blockPrefix := fmt.Sprintf("%010d", startBlock)
	startOffset := statesPrefix + blockPrefix

	zlog.Debug("listing files with lookback",
		zap.Uint64("low_from_binary_search", low),
		zap.Uint64("start_block", startBlock),
		zap.String("start_offset", startOffset),
	)

	query := &storage.Query{
		Prefix:      statesPrefix,
		StartOffset: startOffset,
		Projection:  storage.ProjectionNoACL, // Minimal metadata for fast listing
	}

	// Keep last 10 files in case the latest doesn't have metadata
	const maxFilesToKeep = 10
	var kvFiles []string

	it := bucket.Objects(ctx, query)
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing from block %d: %w", low, err)
		}

		if isStateFile(attrs.Name) {
			kvFiles = append(kvFiles, attrs.Name)
			if len(kvFiles) > maxFilesToKeep {
				kvFiles = kvFiles[1:]
			}
		}
	}

	zlog.Debug("files found with lookback", zap.Int("count", len(kvFiles)))

	if len(kvFiles) == 0 {
		zlog.Debug("no .kv.zst files found")
		return &StoreSizes{}, nil
	}

	// Phase 4: Try latest files until we find one with metadata
	// Files are in ascending order, so iterate backwards from the end
	maxTries := 5
	if len(kvFiles) < maxTries {
		maxTries = len(kvFiles)
	}

	zlog.Debug("checking files for metadata",
		zap.Int("total_files", len(kvFiles)),
		zap.Int("max_tries", maxTries),
		zap.Strings("files_to_check", kvFiles[len(kvFiles)-maxTries:]),
	)

	var uncompressedSize *int64
	var latestFileName string
	var latestFileSize int64

	// Iterate backwards through files until we find one with metadata.
	// Always track the first file we can read (latest) as fallback for compressed size.
	for i := len(kvFiles) - 1; i >= len(kvFiles)-maxTries; i-- {
		fileName := kvFiles[i]
		attrs, err := bucket.Object(fileName).Attrs(ctx)
		if err != nil {
			zlog.Debug("failed to get attrs for file", zap.String("file", fileName), zap.Error(err))
			continue
		}

		zlog.Debug("checking file for metadata",
			zap.String("file", fileName),
			zap.Int64("size", attrs.Size),
			zap.Int("metadata_count", len(attrs.Metadata)),
		)

		// Always record the latest readable file (first one we encounter iterating backwards).
		if latestFileName == "" {
			latestFileName = fileName
			latestFileSize = attrs.Size
		}

		// Check for uncompressed size in custom metadata
		if datasize, ok := attrs.Metadata["datasize"]; ok {
			if size, err := strconv.ParseInt(datasize, 10, 64); err == nil {
				uncompressedSize = &size
				latestFileName = fileName
				latestFileSize = attrs.Size
				zlog.Debug("found datasize metadata",
					zap.String("file", fileName),
					zap.Int64("uncompressed_size", size),
					zap.Int64("compressed_size", attrs.Size),
				)
				break
			}
		}
	}

	if latestFileName == "" {
		zlog.Debug("no files with readable attrs found")
		return &StoreSizes{}, nil
	}

	// Extract just the filename from the full path
	if idx := strings.LastIndex(latestFileName, "/"); idx >= 0 {
		latestFileName = latestFileName[idx+1:]
	}

	zlog.Debug("returning store sizes",
		zap.String("latest_file", latestFileName),
		zap.Int64("compressed_size", latestFileSize),
		zap.Int64p("uncompressed_size", uncompressedSize),
	)

	return &StoreSizes{
		TotalCompressed:  0, // Not calculated in fast mode
		LiveCompressed:   latestFileSize,
		LiveUncompressed: uncompressedSize,
		LatestFile:       latestFileName,
	}, nil
}
