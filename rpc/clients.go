package rpc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hashicorp/go-multierror"
	"go.uber.org/zap"
)

var ErrorNoMoreClient = errors.New("no more clients")
var ErrorNoClientsAvailable = errors.New("no clients available")

type Clients[C any] struct {
	clients               []C
	maxBlockFetchDuration time.Duration
	rollingStrategy       RollingStrategy[C]
	lock                  sync.Mutex
	logger                *zap.Logger
}

func NewClients[C any](maxBlockFetchDuration time.Duration, rollingStrategy RollingStrategy[C], logger *zap.Logger) *Clients[C] {
	return &Clients[C]{
		maxBlockFetchDuration: maxBlockFetchDuration,
		rollingStrategy:       rollingStrategy,
		logger:                logger,
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

			switch s := c.rollingStrategy.(type) {
			case *StickyRollingStrategy[C]:
				s.firstCallToNewClient = true
				s.usedClientCount = 0
				s.nextClientIndex = 0
			case *RollingStrategyAlwaysUseFirst[C]:
				s.nextIndex = 0
			}

			time.Sleep(every)
		}
	}()
}

func (c *Clients[C]) Add(client C) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.clients = append(c.clients, client)
}

func (c *Clients[C]) GetClientByIndex(index int) (C, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	var client C
	if len(c.clients) == 0 {
		return client, ErrorNoClientsAvailable
	}

	if index < 0 || index >= len(c.clients) {
		return client, fmt.Errorf("client index out of range: %d", index)
	}

	return c.clients[index], nil
}

func (c *Clients[C]) GetClientCount() int {
	c.lock.Lock()
	defer c.lock.Unlock()
	return len(c.clients)
}

func (c *Clients[C]) GetMaxBlockFetchDuration() time.Duration {
	return c.maxBlockFetchDuration
}

func WithClientsContext[C any, V any](clients *Clients[C], ctx context.Context, f func(context.Context, C) (v V, err error)) (v V, err error) {
	clients.lock.Lock()
	defer clients.lock.Unlock()
	var errs error

	clients.rollingStrategy.reset()
	client, err := clients.rollingStrategy.next(clients)
	if err != nil {
		errs = multierror.Append(errs, err)
		return v, errs
	}

	for {
		ctx, cancel := context.WithTimeout(ctx, clients.maxBlockFetchDuration)

		v, err := f(ctx, client)
		cancel()

		if err != nil {
			errs = multierror.Append(errs, err)
			client, err = clients.rollingStrategy.next(clients)
			if err != nil {
				errs = multierror.Append(errs, err)
				return v, errs
			}

			continue
		}
		return v, nil
	}
}

func WithClients[C any, V any](clients *Clients[C], f func(context.Context, C) (v V, err error)) (v V, err error) {
	return WithClientsContext(clients, context.Background(), f)
}
