package rpc

import (
	"errors"

	"github.com/hashicorp/go-multierror"
)

var ErrorNoMoreClient = errors.New("no more clients")

type Clients[C any] struct {
	clients []C
	next    int
}

func (c *Clients[C]) Next() (client C, err error) {
	if len(c.clients) <= c.next {
		return client, ErrorNoMoreClient
	}
	client = c.clients[c.next]
	c.next++
	return client, nil
}

func WithClients[C any, V any](clients *Clients[C], f func(C) (v V, err error)) (v V, err error) {
	var errs error
	for {
		client, err := clients.Next()
		if err != nil {
			return v, err
		}
		v, err := f(client)
		if err != nil {
			errs = multierror.Append(errs, err)
			continue
		}
		return v, nil
	}
}
