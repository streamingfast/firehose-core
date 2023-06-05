package firecore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_buildNodeArguments(t *testing.T) {
	dataDir := "/data"
	nodeDataDir := "/data/node"
	hostname := "host"

	tests := []struct {
		name      string
		args      string
		want      []string
		assertion require.ErrorAssertionFunc
	}{
		{"no variables", "arg1 arg2", []string{"arg1", "arg2"}, require.NoError},
		{"variable data-dir", "{data-dir} arg2", []string{"/data", "arg2"}, require.NoError},
		{"variable node-data-dir", "{node-data-dir} arg2", []string{"/data/node", "arg2"}, require.NoError},
		{"variable hostname", "{hostname} arg2", []string{"host", "arg2"}, require.NoError},
		{"variable data-dir double quotes", `"{hostname} with spaces" arg2`, []string{"host with spaces", "arg2"}, require.NoError},
		{"variable all", `--home="{data-dir}" --data={node-data-dir} --id={hostname} --other`, []string{
			"--home=/data",
			"--data=/data/node",
			"--id=host",
			"--other",
		}, require.NoError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildNodeArguments(dataDir, nodeDataDir, hostname, tt.args)
			tt.assertion(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
