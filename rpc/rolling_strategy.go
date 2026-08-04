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

type RollingStrategyAlwaysUseFirst[C any] struct {
	nextIndex int
}

func NewRollingStrategyAlwaysUseFirst[C any]() *RollingStrategyAlwaysUseFirst[C] {
	return &RollingStrategyAlwaysUseFirst[C]{}
}

func (s *RollingStrategyAlwaysUseFirst[C]) reset() {
	s.nextIndex = 0
}

func (s *RollingStrategyAlwaysUseFirst[C]) resetToDeclaredOrder() {
	s.nextIndex = 0
}

func (s *RollingStrategyAlwaysUseFirst[C]) next(c *Clients[C]) (client C, index int, err error) {
	if len(c.clients) <= s.nextIndex {
		return client, 0, ErrorNoMoreClient
	}

	index = s.nextIndex
	client = c.clients[index]
	s.nextIndex++
	return client, index, nil
}
