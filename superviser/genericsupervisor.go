package superviser

import (
	"strings"

	"github.com/ShinyTrinkets/overseer"
	nodeManager "github.com/streamingfast/firehose-core/node-manager"
	"github.com/streamingfast/firehose-core/node-manager/superviser"
	"go.uber.org/zap"
)

var (
	SupervisorFactory = newGenericSupervisor
)

type GenericSuperviser struct {
	*superviser.Superviser

	binary    string
	arguments []string
	name      string
}

// This is the default implementation of the Chain Supervisor. If you wish to override the implementation for
// your given chain you can override the 'SupervisorFactory' variable
func newGenericSupervisor(name, binary string, arguments []string, lineBufferSize uint64, appLogger *zap.Logger) nodeManager.ChainSuperviser {
	if overseer.DEFAULT_LINE_BUFFER_SIZE < int(lineBufferSize) {
		overseer.DEFAULT_LINE_BUFFER_SIZE = int(lineBufferSize)
	}

	return &GenericSuperviser{
		Superviser: superviser.New(appLogger, binary, arguments),
		name:       name,
		binary:     binary,
		arguments:  arguments,
	}
}

func (g *GenericSuperviser) GetCommand() string {
	return g.binary + " " + strings.Join(g.arguments, " ")
}

func (g *GenericSuperviser) GetName() string {
	return g.name
}

func (g *GenericSuperviser) ServerID() (string, error) {
	return "", nil
}
