package mergeblock

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"cloud.google.com/go/storage"
	firecore "github.com/streamingfast/firehose-core"
	"go.uber.org/zap"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/api/option/internaloption"
)

// The custom object metadata entries the merger writes on a merged-blocks file, which
// 'annotate-merged-blocks' backfills and 'stats-merged-blocks' reports from.
const (
	dataSizeMetadataKey     = firecore.MergedBlocksDataSizeMetadataKey
	itemCountMetadataKey    = firecore.MergedBlocksItemCountMetadataKey
	timestampMetadataKey    = firecore.MergedBlocksTimestampMetadataKey
	timestampMetadataLayout = firecore.MergedBlocksTimestampLayout
)

// mergedBlocksFileRE matches a merged-blocks object base name, '0000012300.dbin.zst', with the
// compression suffix optional.
var mergedBlocksFileRE = regexp.MustCompile(`^(\d{10})\.dbin(\.zst)?$`)

// gsTarget is what the store URL says: where to look, plus the two query parameters dstore
// reads off a 'gs://' URL, so the same URL means the same thing here as it does anywhere else
// in the stack.
type gsTarget struct {
	bucket string
	prefix string
	// userProject bills the reads to that project, for a requester-pays bucket ('?project=').
	userProject string
	// grpc is '?client_protocol=grpc', which selects the same transport as --grpc.
	grpc bool
}

func parseGSURL(storeURL string) (gsTarget, error) {
	parsed, err := url.Parse(storeURL)
	if err != nil {
		return gsTarget{}, fmt.Errorf("invalid store url %q: %w", storeURL, err)
	}
	if parsed.Scheme != "gs" {
		return gsTarget{}, fmt.Errorf("store url %q must be a Google Cloud Storage url (gs://bucket/path), this command works on object metadata which only that backend supports", storeURL)
	}
	if parsed.Host == "" {
		return gsTarget{}, fmt.Errorf("store url %q has no bucket", storeURL)
	}

	prefix := strings.Trim(parsed.Path, "/")
	if prefix != "" {
		prefix += "/"
	}

	query := parsed.Query()
	return gsTarget{
		bucket:      parsed.Host,
		prefix:      prefix,
		userProject: query.Get("project"),
		grpc:        query.Get("client_protocol") == "grpc",
	}, nil
}

func newGSClient(ctx context.Context, target gsTarget, useGRPC bool, connectionPool int, logger *zap.Logger) (*storage.Client, error) {
	if !useGRPC {
		return storage.NewClient(ctx)
	}

	opts := []option.ClientOption{option.WithGRPCConnectionPool(connectionPool)}
	if target.userProject != "" {
		// DirectPath goes straight to the storage backend, which does not honour the
		// x-goog-user-project gRPC metadata header requester-pays billing needs. Turning it
		// off keeps gRPC while routing through the Google Front End, which does enforce it.
		opts = append(opts, internaloption.EnableDirectPath(false))
		logger.Warn("DirectPath disabled: it does not carry the requester-pays project, which this store url sets",
			zap.String("project", target.userProject),
		)
	}
	return storage.NewGRPCClient(ctx, opts...)
}

// grpcConnectionPool spreads the readers over several gRPC connections: a single HTTP/2
// connection multiplexes every stream over one TCP socket, which caps throughput well before
// a wide worker pool does.
func grpcConnectionPool(requested, parallelism int) int {
	if requested > 0 {
		return requested
	}
	pool := (parallelism + 31) / 32
	if pool < 1 {
		return 1
	}
	if pool > 16 {
		return 16
	}
	return pool
}

func bucketHandle(client *storage.Client, target gsTarget) *storage.BucketHandle {
	bucket := client.Bucket(target.bucket)
	if target.userProject != "" {
		bucket = bucket.UserProject(target.userProject)
	}
	return bucket
}

// mergedBlocksObject is one merged-blocks file as the listing returned it.
type mergedBlocksObject struct {
	attrs *storage.ObjectAttrs
	// lowBlockNum is the first block of the bundle, the number the file is named after.
	lowBlockNum uint64
	// compressed is false for a plain '.dbin' file.
	compressed bool
}

// walkMergedBlocks lists the merged-blocks files of the store between startBlock and stopBlock,
// calling onObject for each. Both bounds are pushed to the service as listing offsets: names are
// zero-padded to a fixed width, so the lexicographic order the listing walks is the block order.
// stopBlock is exclusive and 0 means no upper bound. Anything under the prefix that is not a
// merged-blocks file is ignored.
func walkMergedBlocks(
	ctx context.Context,
	bucket *storage.BucketHandle,
	prefix string,
	startBlock, stopBlock uint64,
	attrSelection []string,
	onObject func(mergedBlocksObject) error,
) error {
	query := &storage.Query{Prefix: prefix, Delimiter: "/"}
	if startBlock > 0 {
		query.StartOffset = prefix + fmt.Sprintf("%010d", startBlock)
	}
	if stopBlock > 0 {
		query.EndOffset = prefix + fmt.Sprintf("%010d", stopBlock)
	}
	if err := query.SetAttrSelection(attrSelection); err != nil {
		return fmt.Errorf("selecting listing attributes: %w", err)
	}

	it := bucket.Objects(ctx, query)
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return err
		}

		match := mergedBlocksFileRE.FindStringSubmatch(strings.TrimPrefix(attrs.Name, prefix))
		if match == nil {
			continue
		}
		lowBlockNum, err := strconv.ParseUint(match[1], 10, 64)
		if err != nil {
			continue
		}

		object := mergedBlocksObject{attrs: attrs, lowBlockNum: lowBlockNum, compressed: match[2] == ".zst"}
		if err := onObject(object); err != nil {
			return err
		}
	}
}
