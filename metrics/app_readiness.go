package metrics

import (
	"sync"

	"github.com/streamingfast/dmetrics"
)

// AppReadiness is an app's `ready{app="..."}` gauge, exported only by a process that runs
// the app. `dmetrics.Set.NewAppReadiness` exports the gauge at 0 as soon as it is created,
// so creating it in a package-level variable exported `ready` at 0 for every app in every
// process, including apps the process doesn't run.
//
// Declare it at package level as before; the app calls Init when it starts, which exports
// the gauge at 0 until the app is ready. SetReady also initializes it, so it is never nil.
type AppReadiness struct {
	set   *dmetrics.Set
	app   string
	once  sync.Once
	gauge *dmetrics.AppReadiness
}

func NewAppReadiness(set *dmetrics.Set, app string) *AppReadiness {
	return &AppReadiness{set: set, app: app}
}

// Init exports the gauge, at 0 (not ready). Calling it again does nothing.
func (a *AppReadiness) Init() {
	a.once.Do(func() {
		a.gauge = a.set.NewAppReadiness(a.app)
	})
}

func (a *AppReadiness) SetReady() {
	a.Init()
	a.gauge.SetReady()
}

func (a *AppReadiness) SetNotReady() {
	a.Init()
	a.gauge.SetNotReady()
}
