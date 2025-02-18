package rpc

type RollingStrategy[C any] interface {
	reset()
	next(clients *Clients[C]) (C, error)
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

func (s *StickyRollingStrategy[C]) next(clients *Clients[C]) (client C, err error) {
	if len(clients.clients) == s.usedClientCount {
		return client, ErrorNoMoreClient
	}

	if s.firstCallToNewClient {
		s.firstCallToNewClient = false
		client = clients.clients[0]
		s.usedClientCount = s.usedClientCount + 1
		s.nextClientIndex = s.nextClientIndex + 1
		return client, nil
	}

	if s.nextClientIndex == len(clients.clients) { //roll to 1st client
		s.nextClientIndex = 0
	}

	if s.usedClientCount == 0 { //just been reset
		s.nextClientIndex = s.prevIndex(clients)
		client = clients.clients[s.nextClientIndex]
		s.usedClientCount = s.usedClientCount + 1
		s.nextClientIndex = s.nextClientIndex + 1
		return client, nil
	}

	if s.nextClientIndex == len(clients.clients) { //roll to 1st client
		client = clients.clients[0]
		s.usedClientCount = s.usedClientCount + 1
		return client, nil
	}

	client = clients.clients[s.nextClientIndex]
	s.usedClientCount = s.usedClientCount + 1
	s.nextClientIndex = s.nextClientIndex + 1
	return client, nil
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

func (s *RollingStrategyAlwaysUseFirst[C]) next(c *Clients[C]) (client C, err error) {

	if len(c.clients) <= s.nextIndex {
		return client, ErrorNoMoreClient
	}
	client = c.clients[s.nextIndex]
	s.nextIndex++
	return client, nil

}
