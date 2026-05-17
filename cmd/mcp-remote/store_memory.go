package main

import (
	"context"
	"sync"
)

// memoryStore preserves the exact behavior of the pre-Store
// implementation (`sync.Map`s on the `oauthServer`). Used when no
// DynamoDB tables are configured.
//
// No TTL enforcement — entries live until process exit. The oauth
// flow's own time.Now() vs Expires checks gate stale data; the
// memory store will hold them until the GC takes the process.
type memoryStore struct {
	clients sync.Map // clientID -> *registeredClient
	codes   sync.Map // code -> *authorizationCode
	pending sync.Map // state -> *pendingAuth
}

func newMemoryStore() *memoryStore { return &memoryStore{} }

func (m *memoryStore) PutClient(_ context.Context, c *registeredClient) error {
	m.clients.Store(c.ClientID, c)
	return nil
}

func (m *memoryStore) GetClient(_ context.Context, clientID string) (*registeredClient, error) {
	v, ok := m.clients.Load(clientID)
	if !ok {
		return nil, ErrNotFound
	}
	return v.(*registeredClient), nil
}

func (m *memoryStore) PutCode(_ context.Context, c *authorizationCode) error {
	m.codes.Store(c.Code, c)
	return nil
}

func (m *memoryStore) PopCode(_ context.Context, code string) (*authorizationCode, error) {
	v, ok := m.codes.LoadAndDelete(code)
	if !ok {
		return nil, ErrNotFound
	}
	return v.(*authorizationCode), nil
}

func (m *memoryStore) PutPending(_ context.Context, state string, p *pendingAuth) error {
	m.pending.Store(state, p)
	return nil
}

func (m *memoryStore) PopPending(_ context.Context, state string) (*pendingAuth, error) {
	v, ok := m.pending.LoadAndDelete(state)
	if !ok {
		return nil, ErrNotFound
	}
	return v.(*pendingAuth), nil
}
