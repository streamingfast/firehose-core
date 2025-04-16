package apps

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_buildNodeArguments(t *testing.T) {
	dataDir := "/data"
	nodeDataDir := "/data/node"
	hostname := "host"

	envVar := func(k string) string {
		switch k {
		case "myhostname":
			return "host with spaces"
		}

		return ""
	}

	tests := []struct {
		name                 string
		args                 string
		withEnv              func(k string) string
		want                 []string
		firstStreamableBlock uint64
		startBlockNum        uint64
		stopBlockNum         uint64
		assertion            require.ErrorAssertionFunc
	}{
		{"no variables", "arg1 arg2", nil, []string{"arg1", "arg2"}, 0, 10, 20, require.NoError},
		{"variable data-dir", "{data-dir} arg2", nil, []string{"/data", "arg2"}, 0, 10, 20, require.NoError},
		{"variable node-data-dir", "{node-data-dir} arg2", nil, []string{"/data/node", "arg2"}, 0, 10, 20, require.NoError},
		{"variable hostname", "{hostname} arg2", nil, []string{"host", "arg2"}, 0, 10, 20, require.NoError},
		{"variable first-streamable-block", "{first-streamable-block} arg2", nil, []string{"0", "arg2"}, 0, 10, 20, require.NoError},
		{"variable start block num", "{start-block-num} arg2", nil, []string{"10", "arg2"}, 0, 10, 20, require.NoError},
		{"variable stop block num", "{stop-block-num} arg2", nil, []string{"20", "arg2"}, 0, 10, 20, require.NoError},
		{"variable data-dir double quotes", `"{hostname} with spaces" arg2`, nil, []string{"host with spaces", "arg2"}, 0, 10, 20, require.NoError},
		{"variable all", `--home="{data-dir}" --data={node-data-dir} --id={hostname} --other --start={start-block-num} -stop {stop-block-num} --foo`, nil, []string{
			"--home=/data",
			"--data=/data/node",
			"--id=host",
			"--other",
			"--start=10",
			"-stop",
			"20",
			"--foo",
		}, 0, 10, 20, require.NoError},

		{"env variable plain", `--endpoint=${myhostname}`, envVar, []string{"--endpoint=host with spaces"}, 0, 10, 20, require.NoError},
		{"env variable that expand with spaces is split correctly", `"${myhostname}" arg2`, envVar, []string{"host with spaces", "arg2"}, 0, 10, 20, require.NoError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := createNodeArgumentsResolver(dataDir, nodeDataDir, hostname, tt.firstStreamableBlock, tt.startBlockNum, tt.stopBlockNum)

			if tt.withEnv != nil {
				osEnvExpandGetter = tt.withEnv
				t.Cleanup(func() { osEnvExpandGetter = os.Getenv })
			}

			args, err := buildNodeArguments(tt.args, resolver)
			tt.assertion(t, err)

			assert.Equal(t, tt.want, args)
		})
	}
}
