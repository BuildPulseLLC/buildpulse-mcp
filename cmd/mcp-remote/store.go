package main

// Store is the OAuth-server state-persistence abstraction. The OAuth
// 2.1 dance touches state in three places that all need to outlive a
// single HTTP request and may need to survive task replacement:
//
//   - registered clients (RFC 7591 dynamic registration)
//   - authorization codes pending /token exchange (60s TTL)
//   - /authorize state pending the Cognito hop (15-min TTL)
//
// Before this interface existed the state lived in `sync.Map`s on the
// `oauthServer` value. That works at desired_count=1 but breaks during
// rolling deploys (the new task doesn't see the old task's pending
// state) and at desired_count>1 (callbacks can land on a different
// task than the one that started the flow).
//
// Two implementations:
//
//   - memoryStore — `sync.Map`-based, identical behavior to the
//     pre-Store world. Used when no DynamoDB tables are configured
//     (local dev, CI).
//
//   - dynamoStore — backed by the three `${env}-mcp-oauth-{clients,
//     codes,pending}` DynamoDB tables provisioned in
//     environment/dynamodb.tf. Used in production.

import (
	"context"
	"errors"
)

// ErrNotFound is returned by Store.Get* / Store.Pop* when the key
// isn't present (or for Pop, after the TTL has reaped it). Callers
// treat this as "no such entry" — not as a transport error.
var ErrNotFound = errors.New("not found")

// Store is implemented by both the memory and DynamoDB backends.
type Store interface {
	// PutClient creates or replaces the registered client.
	PutClient(ctx context.Context, c *registeredClient) error
	// GetClient returns the registered client, or ErrNotFound.
	GetClient(ctx context.Context, clientID string) (*registeredClient, error)

	// PutCode persists an authorization code (single-use, ~60s TTL).
	PutCode(ctx context.Context, c *authorizationCode) error
	// PopCode atomically reads + deletes the code. Returns ErrNotFound
	// if the code never existed, was already consumed, or has TTL'd.
	PopCode(ctx context.Context, code string) (*authorizationCode, error)

	// PutPending persists state for the Cognito hop (~15-min TTL).
	PutPending(ctx context.Context, state string, p *pendingAuth) error
	// PopPending atomically reads + deletes the pending entry.
	PopPending(ctx context.Context, state string) (*pendingAuth, error)

	// PutRefresh persists a refresh token (~30-day TTL). Implementations
	// with no refresh backend configured (e.g. DynamoDB store without the
	// refresh table) return an error so the caller degrades to "no
	// refresh token issued" rather than pretending it stored one.
	PutRefresh(ctx context.Context, rt *refreshToken) error
	// PopRefresh atomically reads + deletes the refresh token (single-use
	// rotation). Returns ErrNotFound if unknown, already rotated, or TTL'd.
	PopRefresh(ctx context.Context, hashedToken string) (*refreshToken, error)
}
