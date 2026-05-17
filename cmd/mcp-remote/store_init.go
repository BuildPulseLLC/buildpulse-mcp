package main

// buildOAuthStore selects the right Store backend based on the
// running environment. The three OAUTH_* table-name env vars
// (provided by .infra/main.tf from environment/dynamodb.tf via
// outputs.mcp.oauth_*_table) opt the server into the DynamoDB
// backend. When any of them are unset we fall back to the in-memory
// store — the same sync.Map-based behavior the OAuth server had
// before the Store abstraction existed, which is fine for
// desired_count=1 dev/test environments.

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

const (
	envOAuthClientsTable = "OAUTH_CLIENTS_TABLE"
	envOAuthCodesTable   = "OAUTH_CODES_TABLE"
	envOAuthPendingTable = "OAUTH_PENDING_TABLE"
)

func buildOAuthStore(ctx context.Context) Store {
	clients := os.Getenv(envOAuthClientsTable)
	codes := os.Getenv(envOAuthCodesTable)
	pending := os.Getenv(envOAuthPendingTable)

	if clients == "" || codes == "" || pending == "" {
		log.Printf("OAuth store: using in-memory (set OAUTH_CLIENTS_TABLE/OAUTH_CODES_TABLE/OAUTH_PENDING_TABLE for DynamoDB)")
		return newMemoryStore()
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Printf("OAuth store: AWS config load failed (%v) — falling back to in-memory", err)
		return newMemoryStore()
	}
	ddb := dynamodb.NewFromConfig(cfg)
	log.Printf("OAuth store: using DynamoDB (clients=%s, codes=%s, pending=%s)", clients, codes, pending)
	return newDynamoStore(ddb, clients, codes, pending)
}
