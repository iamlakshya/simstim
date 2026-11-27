package supervisor

import (
	"context"
	"net"
	"sync"
)

// requestLifetimesV1 owns actual accepted serve stacks. Closing the admission
// gate precedes cancellation, and every stack joins before native deposit.
// This tracks real IO owners, never a count reconstructed from saved state.
type requestLifetimesV1 struct {
	mu          sync.Mutex
	closed      bool
	connections map[*requestConnectionV1]struct{}
	workers     sync.WaitGroup
}

type requestConnectionV1 struct {
	connection net.Conn
	client     *client
	once       sync.Once
}

func (s *Supervisor) ownRequestConnectionV1(connection net.Conn) (*requestConnectionV1, bool) {
	root := s.owner()
	root.requests.mu.Lock()
	defer root.requests.mu.Unlock()
	if root.requests.closed {
		_ = connection.Close()
		return nil, false
	}
	owned := &requestConnectionV1{connection: connection}
	if root.requests.connections == nil {
		root.requests.connections = make(map[*requestConnectionV1]struct{})
	}
	root.requests.connections[owned] = struct{}{}
	root.requests.workers.Add(1)
	return owned, true
}

func (s *Supervisor) bindRequestClientV1(owned *requestConnectionV1, client *client) {
	root := s.owner()
	root.requests.mu.Lock()
	owned.client = client
	root.requests.mu.Unlock()
}

func (s *Supervisor) finishRequestConnectionV1(owned *requestConnectionV1) {
	root := s.owner()
	owned.once.Do(func() {
		root.requests.mu.Lock()
		delete(root.requests.connections, owned)
		root.requests.mu.Unlock()
		root.requests.workers.Done()
	})
}

func (s *Supervisor) cancelRequestConnectionsV1() {
	root := s.owner()
	root.requests.mu.Lock()
	root.requests.closed = true
	connections := make([]net.Conn, 0, len(root.requests.connections))
	clients := make([]*client, 0, len(root.requests.connections))
	for owned := range root.requests.connections {
		connections = append(connections, owned.connection)
		if owned.client != nil {
			clients = append(clients, owned.client)
		}
	}
	root.requests.mu.Unlock()
	for _, client := range clients {
		if client.cancel != nil {
			client.cancel()
		}
		if scope := client.nativeScopeV1(); scope != nil {
			scope.mu.Lock()
			cancel := scope.warmCancel
			scope.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		}
	}
	for _, connection := range connections {
		_ = connection.Close()
	}
}

func (s *Supervisor) joinRequestConnectionsV1() { s.owner().requests.workers.Wait() }

// requestContextV1 narrows work to its actual accepted connection. It never
// changes the physical native owner's lifetime or supplies a startup grant.
func (client *client) requestContextV1(parent context.Context) context.Context {
	if client != nil && client.ctx != nil {
		return client.ctx
	}
	return parent
}
