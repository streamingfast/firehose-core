package launcher

import (
	"fmt"

	"github.com/spf13/cobra"
)

type AppDef struct {
	ID            string
	Title         string
	Description   string
	RegisterFlags func(cmd *cobra.Command) error
	InitFunc      func(runtime *Runtime) error
	FactoryFunc   func(runtime *Runtime) (App, error)

	// NotRunByDefault, when true, leaves the app out of `start` when no app is named
	// or when `all` is used; it runs only when named explicitly.
	NotRunByDefault bool
}

func (a *AppDef) String() string {
	return fmt.Sprintf("%s (%s)", a.ID, a.Title)
}

type App interface {
	Terminating() <-chan struct{}
	Terminated() <-chan struct{}
	Shutdown(err error)
	Err() error
	Run() error
}

//go:generate go-enum -f=$GOFILE --marshal --names

// ENUM(
//
//	NotFound
//	Created
//	Running
//	Warning
//	Stopped
//
// )
type AppStatus uint

type AppInfo struct {
	ID     string
	Status AppStatus
}
