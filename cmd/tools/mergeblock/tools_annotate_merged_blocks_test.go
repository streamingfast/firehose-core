package mergeblock

import (
	"bytes"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGSURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		expect    gsTarget
		expectErr string
	}{
		{name: "bucket and folder", url: "gs://example-bucket/eth-mainnet/merged", expect: gsTarget{bucket: "example-bucket", prefix: "eth-mainnet/merged/"}},
		{name: "trailing slash", url: "gs://example-bucket/eth-mainnet/merged/", expect: gsTarget{bucket: "example-bucket", prefix: "eth-mainnet/merged/"}},
		{name: "bucket root", url: "gs://example-bucket", expect: gsTarget{bucket: "example-bucket"}},
		{name: "requester pays project", url: "gs://example-bucket/merged?project=my-project", expect: gsTarget{bucket: "example-bucket", prefix: "merged/", userProject: "my-project"}},
		{name: "grpc client protocol", url: "gs://example-bucket/merged?client_protocol=grpc", expect: gsTarget{bucket: "example-bucket", prefix: "merged/", grpc: true}},
		{name: "http client protocol", url: "gs://example-bucket/merged?client_protocol=http", expect: gsTarget{bucket: "example-bucket", prefix: "merged/"}},
		{name: "both parameters", url: "gs://example-bucket/merged?project=my-project&client_protocol=grpc", expect: gsTarget{bucket: "example-bucket", prefix: "merged/", userProject: "my-project", grpc: true}},
		{name: "local path refused", url: "/data/merged", expectErr: "must be a Google Cloud Storage url"},
		{name: "s3 refused", url: "s3://example-bucket/merged", expectErr: "must be a Google Cloud Storage url"},
		{name: "no bucket", url: "gs:///merged", expectErr: "has no bucket"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target, err := parseGSURL(test.url)
			if test.expectErr != "" {
				require.ErrorContains(t, err, test.expectErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.expect, target)
		})
	}
}

func TestMergedBlocksFileRegex(t *testing.T) {
	tests := []struct {
		base      string
		matches   bool
		zstSuffix string
	}{
		{base: "0000012300.dbin.zst", matches: true, zstSuffix: ".zst"},
		{base: "0000012300.dbin", matches: true, zstSuffix: ""},
		{base: "0000012300.dbin.gz", matches: false},
		{base: "123.dbin.zst", matches: false},
		{base: "0000012300-0000012400.output.zst", matches: false},
		{base: "substreams.spkg.zst", matches: false},
	}

	for _, test := range tests {
		t.Run(test.base, func(t *testing.T) {
			match := mergedBlocksFileRE.FindStringSubmatch(test.base)
			if !test.matches {
				assert.Nil(t, match)
				return
			}
			require.NotNil(t, match)
			assert.Equal(t, test.zstSuffix, match[2])
		})
	}
}

func TestCountDecompressed(t *testing.T) {
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
	require.NoError(t, err)
	defer decoder.Close()

	// Reusing one decoder across files is what the workers do, so count twice.
	for _, payloadSize := range []int{0, 1024, 5 * 1024 * 1024} {
		payload := bytes.Repeat([]byte("firehose merged blocks payload "), payloadSize/31+1)[:payloadSize]

		var compressed bytes.Buffer
		encoder, err := zstd.NewWriter(&compressed)
		require.NoError(t, err)
		_, err = encoder.Write(payload)
		require.NoError(t, err)
		require.NoError(t, encoder.Close())

		size, err := countDecompressed(decoder, bytes.NewReader(compressed.Bytes()))
		require.NoError(t, err)
		assert.Equal(t, int64(payloadSize), size)
	}
}

func TestGRPCConnectionPool(t *testing.T) {
	assert.Equal(t, 7, grpcConnectionPool(annotateConfig{parallelism: 32, connPool: 7}))
	assert.Equal(t, 1, grpcConnectionPool(annotateConfig{parallelism: 1}))
	assert.Equal(t, 1, grpcConnectionPool(annotateConfig{parallelism: 32}))
	assert.Equal(t, 2, grpcConnectionPool(annotateConfig{parallelism: 33}))
	assert.Equal(t, 16, grpcConnectionPool(annotateConfig{parallelism: 4096}))
}
