package nodemanager

import (
	"strings"

	"github.com/ShinyTrinkets/overseer"
	nodeManager "github.com/streamingfast/node-manager"
	"github.com/streamingfast/node-manager/superviser"
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
func newGenericSupervisor(name, binary string, arguments []string, appLogger *zap.Logger) nodeManager.ChainSuperviser {
	overseer.DEFAULT_LINE_BUFFER_SIZE = 50 * 1024 * 1024

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
