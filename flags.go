package firecore

import (
	"fmt"
	"os"
	"sync"

	"github.com/spf13/cobra"
)

var hideGlobalFlagsWarnOnce sync.Once

// HideGlobalFlagsOnChildCmd used to hide the noisiest global flags from the help output of
// child commands.
//
// Deprecated: This is now a no-op. Every command except the root command and 'start' hides
// all the global flags behind a single '--gh' ('--global-help') flag, which makes hiding a
// hand-picked subset of them useless. It will be removed in a future version.
func HideGlobalFlagsOnChildCmd(_ *cobra.Command) {
	hideGlobalFlagsWarnOnce.Do(func() {
		fmt.Fprintln(os.Stderr, "DEPRECATED: 'firecore.HideGlobalFlagsOnChildCmd' is a no-op and can be removed, global flags are now hidden behind the '--gh' flag on all commands except the root command and 'start'")
	})
}
