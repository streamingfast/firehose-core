package rpc

type RollingStrategy[C any] interface {
	// reset prepares the strategy for a new [WithClientsContext] call, it does **not**
	// change which client is going to be returned first, use [resetToDeclaredOrder] for
	// that.
	reset()

	// resetToDeclaredOrder brings the strategy back to its initial state so that the next
	// call to [next] returns the first client of the pool, in declared (or last sorted)
	// order. It's the "failback" operation, exposed publicly through [Clients.Reset].
	resetToDeclaredOrder()

	// next returns the client to use next, alongside its index in the pool so that the
	// caller can resolve the client's provider name.
	next(clients *Clients[C]) (C, int, error)
}

type StickyRollingStrategy[C any] struct {
	firstCallToNewClient bool
	usedClientCount      int
	nextClientIndex      int
}

func NewStickyRollingStrategy[C any]() *StickyRollingStrategy[C] {
	return &StickyRollingStrategy[C]{
		firstCallToNewClient: true,
	}
}

func (s *StickyRollingStrategy[C]) reset() {
	s.usedClientCount = 0
}

func (s *StickyRollingStrategy[C]) resetToDeclaredOrder() {
	s.firstCallToNewClient = true
	s.usedClientCount = 0
	s.nextClientIndex = 0
}

func (s *StickyRollingStrategy[C]) next(clients *Clients[C]) (client C, index int, err error) {
	if len(clients.clients) == s.usedClientCount {
		return client, 0, ErrorNoMoreClient
	}

	if s.firstCallToNewClient {
		s.firstCallToNewClient = false
		client = clients.clients[0]
		s.usedClientCount = s.usedClientCount + 1
		s.nextClientIndex = s.nextClientIndex + 1
		return client, 0, nil
	}

	if s.nextClientIndex == len(clients.clients) { //roll to 1st client
		s.nextClientIndex = 0
	}

	if s.usedClientCount == 0 { //just been reset
		s.nextClientIndex = s.prevIndex(clients)
		index = s.nextClientIndex
		client = clients.clients[index]
		s.usedClientCount = s.usedClientCount + 1
		s.nextClientIndex = s.nextClientIndex + 1
		return client, index, nil
	}

	index = s.nextClientIndex
	client = clients.clients[index]
	s.usedClientCount = s.usedClientCount + 1
	s.nextClientIndex = s.nextClientIndex + 1
	return client, index, nil
}

func (s *StickyRollingStrategy[C]) prevIndex(clients *Clients[C]) int {
	if s.nextClientIndex == 0 {
		return len(clients.clients) - 1
	}
	return s.nextClientIndex - 1
}

// RollingStrategyAlwaysUseFirst is a deprecated alias, use [RollingStrategySequential] instead.
//
// Deprecated: use [RollingStrategySequential] via [NewRollingStrategySequential] instead.
type RollingStrategyAlwaysUseFirst[C any] = RollingStrategySequential[C]

// NewRollingStrategyAlwaysUseFirst returns a [RollingStrategySequential] with no
// options, matching this constructor's original (pre-[WithSpreadStart]) behavior:
// every call starts at the pool's first client.
//
// Deprecated: use [NewRollingStrategySequential] instead.
func NewRollingStrategyAlwaysUseFirst[C any]() *RollingStrategyAlwaysUseFirst[C] {
	return NewRollingStrategySequential[C]()
}

type rollingStrategySequentialOptions struct {
	spreadStart bool
}

// RollingStrategySequentialOption configures [NewRollingStrategySequential].
type RollingStrategySequentialOption func(*rollingStrategySequentialOptions)

// WithSpreadStart makes the strategy spread its *starting* client round-robin
// across successive [WithClientsContext] calls instead of always starting at
// the pool's first client, while preserving the pool's declared (or last
// sorted) order for failover once a call has picked its start. It's for pools
// used by batched/parallel polling, where concurrent calls would otherwise all
// start on the same provider — unlike [StickyRollingStrategy], a call never
// stays on the provider a previous call ended up rolling to, so this should
// not be used where staying on a known-good provider matters more than
// spreading load.
func WithSpreadStart() RollingStrategySequentialOption {
	return func(o *rollingStrategySequentialOptions) {
		o.spreadStart = true
	}
}

// RollingStrategySequential walks the pool in order (declared, or last sorted)
// starting from a configurable client, rolling forward through the rest of the
// pool for failover. Without options it always starts at the pool's first
// client; see [WithSpreadStart] to spread the starting client across calls
// instead.
type RollingStrategySequential[C any] struct {
	nextStartIndex  int
	startIndex      int
	usedClientCount int
	spreadStart     bool
}

func NewRollingStrategySequential[C any](opts ...RollingStrategySequentialOption) *RollingStrategySequential[C] {
	var cfg rollingStrategySequentialOptions
	for _, opt := range opts {
		opt(&cfg)
	}

	return &RollingStrategySequential[C]{spreadStart: cfg.spreadStart}
}

func (s *RollingStrategySequential[C]) reset() {
	s.startIndex = s.nextStartIndex
	s.usedClientCount = 0
}

func (s *RollingStrategySequential[C]) resetToDeclaredOrder() {
	s.nextStartIndex = 0
	s.startIndex = 0
	s.usedClientCount = 0
}

func (s *RollingStrategySequential[C]) next(clients *Clients[C]) (client C, index int, err error) {
	count := len(clients.clients)
	if s.usedClientCount == count {
		return client, 0, ErrorNoMoreClient
	}

	index = (s.startIndex + s.usedClientCount) % count
	client = clients.clients[index]
	s.usedClientCount++

	if s.spreadStart && s.usedClientCount == 1 {
		s.nextStartIndex = (s.startIndex + 1) % count
	}

	return client, index, nil
}
