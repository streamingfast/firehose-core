package rpc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/hashicorp/go-multierror"
	"go.uber.org/zap"
)

var ErrorNoMoreClient = errors.New("no more clients")

// rollLogInterval is how often at most a given `from -> to` roll is logged at `warn`
// level, see [Clients.logRoll].
const rollLogInterval = 30 * time.Second

type Clients[C any] struct {
	clients               []C
	names                 []string
	maxBlockFetchDuration time.Duration
	rollingStrategy       RollingStrategy[C]
	lock                  sync.Mutex
	logger                *zap.Logger

	// rollLoggedAt is the last time a given `from -> to` roll was logged at `warn` level.
	rollLoggedAt map[string]time.Time
}

func NewClients[C any](maxBlockFetchDuration time.Duration, rollingStrategy RollingStrategy[C], logger *zap.Logger) *Clients[C] {
	if logger == nil {
		logger = zap.NewNop()
	}

	return &Clients[C]{
		maxBlockFetchDuration: maxBlockFetchDuration,
		rollingStrategy:       rollingStrategy,
		logger:                logger,

		rollLoggedAt: map[string]time.Time{},
	}
}

func (c *Clients[C]) StartSorting(ctx context.Context, direction SortDirection, sortValueFetcher SortValueFetcher[C], every time.Duration) {
	go func() {
		for {
			c.logger.Info("sorting clients")
			err := Sort(ctx, c, sortValueFetcher, direction)
			if err != nil {
				c.logger.Warn("sorting", zap.Error(err))
			}

			c.Reset()

			time.Sleep(every)
		}
	}()
}

// Add appends a client to the pool, giving it the auto-generated provider name
// `client-<index>` where `<index>` is its position in the pool. Use [Clients.AddNamed]
// to give the client a meaningful name, which is then reported in errors and logs.
func (c *Clients[C]) Add(client C) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.add(client, fmt.Sprintf("client-%d", len(c.clients)))
}

// AddNamed appends a client to the pool under the provider name `name`, which is
// reported in errors and logs when this client is used and fails.
func (c *Clients[C]) AddNamed(client C, name string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.add(client, name)
}

func (c *Clients[C]) add(client C, name string) {
	c.clients = append(c.clients, client)
	c.names = append(c.names, name)
}

// Names returns the provider names of all clients in the pool, in pool order (which
// changes when the pool is sorted, see [Clients.StartSorting]).
func (c *Clients[C]) Names() []string {
	c.lock.Lock()
	defer c.lock.Unlock()

	return slices.Clone(c.names)
}

// Reset brings the rolling strategy back to the pool's declared order, so that the
// next call to [WithClients]/[WithClientsContext] starts again from the first client
// of the pool. It's the failback operation: a sticky strategy that rolled to a
// fallback provider after a transient error would otherwise stay on that fallback
// until the process restarts.
func (c *Clients[C]) Reset() {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.rollingStrategy.resetToDeclaredOrder()
}

// nameAt returns the provider name of the client at index `index`, the caller is
// responsible of holding the lock.
func (c *Clients[C]) nameAt(index int) string {
	if index < 0 || index >= len(c.names) {
		return fmt.Sprintf("client-%d", index)
	}

	return c.names[index]
}

// logRoll reports that we rolled from provider `from` to provider `to` because of
// `err`. A given `from -> to` pair is logged at `warn` level again only once
// [rollLogInterval] elapsed since the last time it was, the rolls in between going to
// `debug` level. Without this, a pool using [RollingStrategyAlwaysUseFirst] whose
// first provider is down would emit one warning on every single RPC call.
func (c *Clients[C]) logRoll(from string, to string, err error) {
	key := from + " -> " + to

	c.lock.Lock()
	if c.rollLoggedAt == nil {
		c.rollLoggedAt = map[string]time.Time{}
	}

	now := time.Now()
	logIt := now.Sub(c.rollLoggedAt[key]) > rollLogInterval
	if logIt {
		c.rollLoggedAt[key] = now
	}
	c.lock.Unlock()

	log := c.logger.Debug
	if logIt {
		log = c.logger.Warn
	}

	log("rolling to next RPC provider",
		zap.String("from_provider", from),
		zap.String("to_provider", to),
		zap.Error(err),
	)
}

func WithClientsContext[C any, V any](clients *Clients[C], ctx context.Context, f func(context.Context, C) (v V, err error)) (v V, err error) {
	var errs error

	// guard the rolling-strategy state (client selection), NOT the call to f
	clients.lock.Lock()
	clients.rollingStrategy.reset()
	client, index, err := clients.rollingStrategy.next(clients)
	var name string
	if err == nil {
		name = clients.nameAt(index)
	}
	clients.lock.Unlock()
	if err != nil {
		errs = multierror.Append(errs, err)
		return v, errs
	}

	for {
		ctx, cancel := context.WithTimeout(ctx, clients.maxBlockFetchDuration)

		v, err := f(ctx, client)
		cancel()

		if err != nil {
			errs = multierror.Append(errs, fmt.Errorf("provider %q: %w", name, err))

			clients.lock.Lock()
			nextClient, nextIndex, nextErr := clients.rollingStrategy.next(clients)
			var nextName string
			if nextErr == nil {
				nextName = clients.nameAt(nextIndex)
			}
			clients.lock.Unlock()
			if nextErr != nil {
				errs = multierror.Append(errs, nextErr)
				return v, errs
			}

			clients.logRoll(name, nextName, err)

			client, name = nextClient, nextName
			continue
		}
		return v, nil
	}
}

func WithClients[C any, V any](clients *Clients[C], f func(context.Context, C) (v V, err error)) (v V, err error) {
	return WithClientsContext(clients, context.Background(), f)
}
