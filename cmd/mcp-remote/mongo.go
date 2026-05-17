package main

// DocumentDB client for the hosted MCP server. Used by the OAuth
// callback to (a) look up Cognito users in the `users` collection,
// (b) enumerate their `memberships` to discover accessible orgs,
// and (c) persist `mcpSessions` records that platform-api's auth
// middleware reads on every tool call.
//
// The MCP itself doesn't query analytics collections — those still
// go through platform-api's HTTP surface. DocumentDB access here is
// scoped narrowly to user-identity / session-issuance concerns.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// dbClient is initialized once at startup. nil if MONGODB_URI is
// not set — in which case the OAuth callback path degrades to
// returning a token that won't authenticate against platform-api.
// This keeps the OAuth scaffold functional locally / in dev without
// requiring DocumentDB access.
var dbClient *mongo.Client

// initMongo dials DocumentDB and pings to verify. On any failure
// (missing URI, network, auth) we log and continue without DB —
// callers check dbClient != nil before using it.
func initMongo(ctx context.Context) {
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		log.Printf("MONGODB_URI not set; OAuth-minted tokens will NOT be persisted in mcpSessions")
		return
	}

	opts := options.Client().ApplyURI(uri).
		// Same tuning the platform-api uses — see
		// platform-api/internal/mongodb/client.go.
		SetServerSelectionTimeout(5 * time.Second).
		SetConnectTimeout(10 * time.Second).
		SetRetryWrites(false) // DocumentDB 5.0 doesn't support retryWrites

	c, err := mongo.Connect(opts)
	if err != nil {
		log.Printf("failed to connect to DocumentDB: %v (mcpSessions disabled)", err)
		return
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx, nil); err != nil {
		log.Printf("DocumentDB ping failed: %v (mcpSessions disabled)", err)
		_ = c.Disconnect(context.Background())
		return
	}

	dbClient = c
	log.Printf("connected to DocumentDB (mcpSessions enabled)")
}

// dbDatabase returns the right `production` / `development` database
// per the ENVIRONMENT env var, matching the rest of the BuildPulse stack.
func dbDatabase() *mongo.Database {
	if dbClient == nil {
		return nil
	}
	env := os.Getenv("ENVIRONMENT")
	if env == "" {
		env = "development"
	}
	return dbClient.Database(env)
}

// resolveUserOrgs looks up the Cognito user in the `users` collection
// by `id` (which the cognito-lambdas pre-sign-up trigger sets to the
// Cognito sub) and returns the UUIDs of every NON-SANDBOX organization
// they are an active member of.
//
// Returns a non-nil empty slice for users who have no qualifying
// memberships (so callers can distinguish "user exists but no org"
// from "user not found / DB unavailable" via the error).
func resolveUserOrgs(ctx context.Context, cognitoSub, cognitoEmail string) ([]string, error) {
	db := dbDatabase()
	if db == nil {
		return nil, errors.New("DocumentDB not configured")
	}

	// Verify the user record exists. We don't strictly need fields from
	// it for the session mapping, but the existence check protects
	// against minting sessions for users who haven't run through
	// pre-sign-up yet.
	var user struct {
		ID string `bson:"id"`
	}
	err := db.Collection("users").FindOne(ctx, bson.M{"id": cognitoSub}).Decode(&user)
	if err != nil {
		// Fall back to email lookup for users whose `id` field hasn't
		// been migrated from the legacy `google_XXX` Cognito username
		// to the modern `sub` UUID (per cognito-lambdas
		// MigrateMembershipUserId in pre-token-generation).
		err = db.Collection("users").FindOne(ctx, bson.M{"email": cognitoEmail}).Decode(&user)
		if err != nil {
			return nil, fmt.Errorf("no user record for sub=%s or email=%s: %w", cognitoSub, cognitoEmail, err)
		}
	}

	// Enumerate memberships. Filter:
	//   - userId == user.id (UUID)
	//   - status == "active"
	//   - sandbox is not true
	cursor, err := db.Collection("memberships").Find(ctx, bson.M{
		"userId": user.ID,
		"status": "active",
		"sandbox": bson.M{
			"$ne": true,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("memberships query: %w", err)
	}
	defer cursor.Close(ctx)

	orgIDs := make([]string, 0, 4)
	for cursor.Next(ctx) {
		var m struct {
			OrganizationID string `bson:"organizationId"`
		}
		if err := cursor.Decode(&m); err != nil {
			continue
		}
		if m.OrganizationID != "" {
			orgIDs = append(orgIDs, m.OrganizationID)
		}
	}
	return orgIDs, nil
}

// persistMCPSession writes a session record that platform-api's
// auth middleware will accept. The token is hashed before storage
// (same scheme as apiTokens: base64(sha256(token))) — the plaintext
// never lives at rest. Returns the plaintext token for the caller
// to hand back to the OAuth client.
func persistMCPSession(ctx context.Context, plaintextToken, cognitoSub, cognitoEmail string, orgIDs []string, ttl time.Duration) error {
	db := dbDatabase()
	if db == nil {
		return errors.New("DocumentDB not configured")
	}

	sum := sha256.Sum256([]byte(plaintextToken))
	hashed := base64.StdEncoding.EncodeToString(sum[:])

	now := time.Now()
	_, err := db.Collection("mcpSessions").InsertOne(ctx, bson.M{
		"hashedToken":     hashed,
		"organizationIds": orgIDs,
		"cognitoSub":      cognitoSub,
		"cognitoEmail":    cognitoEmail,
		"createdAt":       now,
		"expiresAt":       now.Add(ttl),
	})
	if err != nil {
		return fmt.Errorf("insert mcpSession: %w", err)
	}
	return nil
}
