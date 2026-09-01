package mergeblock

import (
	"testing"

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

func TestGRPCConnectionPool(t *testing.T) {
	assert.Equal(t, 7, grpcConnectionPool(7, 32))
	assert.Equal(t, 1, grpcConnectionPool(0, 1))
	assert.Equal(t, 1, grpcConnectionPool(0, 32))
	assert.Equal(t, 2, grpcConnectionPool(0, 33))
	assert.Equal(t, 16, grpcConnectionPool(0, 4096))
}
