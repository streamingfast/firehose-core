package apps

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/streamingfast/firehose-core/launcher"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSharedLiveHubNotShared(t *testing.T) {
	tests := []struct {
		name                 string
		apps                 []string
		blockStreamAddr      string
		discardPartialBlocks bool
	}{
		{name: "firehose alone", apps: []string{"firehose", "merger"}, blockStreamAddr: ":10014"},
		{name: "tier1 alone", apps: []string{"substreams-tier1", "substreams-tier2"}, blockStreamAddr: ":10014"},
		{name: "no live source", apps: []string{"firehose", "substreams-tier1"}},
		{name: "firehose discards partial blocks", apps: []string{"firehose", "substreams-tier1"}, blockStreamAddr: ":10014", discardPartialBlocks: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Set("common-live-blocks-addr", tt.blockStreamAddr)
			viper.Set("firehose-discard-partial-blocks", tt.discardPartialBlocks)
			t.Cleanup(func() {
				viper.Set("common-live-blocks-addr", nil)
				viper.Set("firehose-discard-partial-blocks", nil)
			})

			h, err := sharedLiveHub(&launcher.Runtime{Apps: tt.apps})
			require.NoError(t, err)
			assert.Nil(t, h)
		})
	}
}
