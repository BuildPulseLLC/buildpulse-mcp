package main

// DynamoDB-backed Store implementation. Used in production so the
// OAuth flow survives rolling deploys + horizontal scaling. See
// store.go for the interface and the design context.
//
// Three tables, each with `client_id`/`code`/`state` as the hash key
// (provisioned by environment/dynamodb.tf):
//
//   - mcp-oauth-clients  durable, no TTL
//   - mcp-oauth-codes    `expires_unix` Unix-epoch TTL (~60s)
//   - mcp-oauth-pending  `expires_unix` Unix-epoch TTL (~15min)
//   - mcp-oauth-refresh  `expires_unix` Unix-epoch TTL (~30d)
//
// Pop* uses DeleteItem with ReturnValues=ALL_OLD so the read + delete
// is atomic — important for single-use authorization codes and for
// single-use refresh-token rotation.

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// dynamoClientItem mirrors registeredClient with a flat shape for
// DynamoDB serialization. We don't `dynamodbav` the original struct
// because RegisteredClient is also serialized into the /oauth/register
// HTTP response with `json:` tags; keeping the two encodings separate
// keeps each tidy.
type dynamoClientItem struct {
	ClientID     string   `dynamodbav:"client_id"`
	ClientName   string   `dynamodbav:"client_name"`
	RedirectURIs []string `dynamodbav:"redirect_uris"`
	GrantTypes   []string `dynamodbav:"grant_types"`
	CreatedUnix  int64    `dynamodbav:"created_unix"`
}

type dynamoCodeItem struct {
	Code            string   `dynamodbav:"code"`
	ClientID        string   `dynamodbav:"client_id"`
	RedirectURI     string   `dynamodbav:"redirect_uri"`
	CodeChallenge   string   `dynamodbav:"code_challenge"`
	Scope           string   `dynamodbav:"scope"`
	UserSubject     string   `dynamodbav:"user_subject"`
	UserEmail       string   `dynamodbav:"user_email"`
	OrganizationIDs []string `dynamodbav:"organization_ids,omitempty"`
	ExpiresUnix     int64    `dynamodbav:"expires_unix"`
}

type dynamoPendingItem struct {
	State         string `dynamodbav:"state"`
	ClientID      string `dynamodbav:"client_id"`
	RedirectURI   string `dynamodbav:"redirect_uri"`
	CodeChallenge string `dynamodbav:"code_challenge"`
	OriginalState string `dynamodbav:"original_state"`
	Scope         string `dynamodbav:"scope"`
	ExpiresUnix   int64  `dynamodbav:"expires_unix"`
}

type dynamoRefreshItem struct {
	// HashedToken is base64(sha256(refresh token)) — the plaintext
	// refresh token never lands in the table (same discipline as
	// mcpSessions). The upstream Cognito refresh token IS stored, but
	// application-encrypted (KMS) in CognitoRefreshEnc.
	HashedToken       string   `dynamodbav:"hashed_token"`
	ClientID          string   `dynamodbav:"client_id"`
	Scope             string   `dynamodbav:"scope"`
	UserSubject       string   `dynamodbav:"user_subject"`
	UserEmail         string   `dynamodbav:"user_email"`
	OrganizationIDs   []string `dynamodbav:"organization_ids,omitempty"`
	CognitoRefreshEnc string   `dynamodbav:"cognito_refresh_enc"`
	ExpiresUnix       int64    `dynamodbav:"expires_unix"`
}

type dynamoStore struct {
	client       *dynamodb.Client
	clientsTable string
	codesTable   string
	pendingTable string
	refreshTable string
}

func newDynamoStore(client *dynamodb.Client, clientsTable, codesTable, pendingTable, refreshTable string) *dynamoStore {
	return &dynamoStore{
		client:       client,
		clientsTable: clientsTable,
		codesTable:   codesTable,
		pendingTable: pendingTable,
		refreshTable: refreshTable,
	}
}

// --- clients --------------------------------------------------------------

func (d *dynamoStore) PutClient(ctx context.Context, c *registeredClient) error {
	item, err := attributevalue.MarshalMap(dynamoClientItem{
		ClientID:     c.ClientID,
		ClientName:   c.ClientName,
		RedirectURIs: c.RedirectURIs,
		GrantTypes:   c.GrantTypes,
		CreatedUnix:  c.CreatedAt.Unix(),
	})
	if err != nil {
		return fmt.Errorf("marshal client: %w", err)
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.clientsTable),
		Item:      item,
	})
	return err
}

func (d *dynamoStore) GetClient(ctx context.Context, clientID string) (*registeredClient, error) {
	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(d.clientsTable),
		Key: map[string]types.AttributeValue{
			"client_id": &types.AttributeValueMemberS{Value: clientID},
		},
	})
	if err != nil {
		return nil, err
	}
	if len(out.Item) == 0 {
		return nil, ErrNotFound
	}
	var raw dynamoClientItem
	if err := attributevalue.UnmarshalMap(out.Item, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal client: %w", err)
	}
	return &registeredClient{
		ClientID:     raw.ClientID,
		ClientName:   raw.ClientName,
		RedirectURIs: raw.RedirectURIs,
		GrantTypes:   raw.GrantTypes,
		CreatedAt:    unixToTime(raw.CreatedUnix),
	}, nil
}

// --- authorization codes --------------------------------------------------

func (d *dynamoStore) PutCode(ctx context.Context, c *authorizationCode) error {
	item, err := attributevalue.MarshalMap(dynamoCodeItem{
		Code:            c.Code,
		ClientID:        c.ClientID,
		RedirectURI:     c.RedirectURI,
		CodeChallenge:   c.CodeChallenge,
		Scope:           c.Scope,
		UserSubject:     c.UserSubject,
		UserEmail:       c.UserEmail,
		OrganizationIDs: c.OrganizationIDs,
		ExpiresUnix:     c.Expires.Unix(),
	})
	if err != nil {
		return fmt.Errorf("marshal code: %w", err)
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.codesTable),
		Item:      item,
	})
	return err
}

func (d *dynamoStore) PopCode(ctx context.Context, code string) (*authorizationCode, error) {
	out, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(d.codesTable),
		Key: map[string]types.AttributeValue{
			"code": &types.AttributeValueMemberS{Value: code},
		},
		ReturnValues: types.ReturnValueAllOld,
	})
	if err != nil {
		return nil, err
	}
	if len(out.Attributes) == 0 {
		return nil, ErrNotFound
	}
	var raw dynamoCodeItem
	if err := attributevalue.UnmarshalMap(out.Attributes, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal code: %w", err)
	}
	return &authorizationCode{
		Code:            raw.Code,
		ClientID:        raw.ClientID,
		RedirectURI:     raw.RedirectURI,
		CodeChallenge:   raw.CodeChallenge,
		Scope:           raw.Scope,
		UserSubject:     raw.UserSubject,
		UserEmail:       raw.UserEmail,
		OrganizationIDs: raw.OrganizationIDs,
		Expires:         unixToTime(raw.ExpiresUnix),
	}, nil
}

// --- pending Cognito state ------------------------------------------------

func (d *dynamoStore) PutPending(ctx context.Context, state string, p *pendingAuth) error {
	item, err := attributevalue.MarshalMap(dynamoPendingItem{
		State:         state,
		ClientID:      p.ClientID,
		RedirectURI:   p.RedirectURI,
		CodeChallenge: p.CodeChallenge,
		OriginalState: p.OriginalState,
		Scope:         p.Scope,
		ExpiresUnix:   p.Expires.Unix(),
	})
	if err != nil {
		return fmt.Errorf("marshal pending: %w", err)
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.pendingTable),
		Item:      item,
	})
	return err
}

func (d *dynamoStore) PopPending(ctx context.Context, state string) (*pendingAuth, error) {
	out, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(d.pendingTable),
		Key: map[string]types.AttributeValue{
			"state": &types.AttributeValueMemberS{Value: state},
		},
		ReturnValues: types.ReturnValueAllOld,
	})
	if err != nil {
		return nil, err
	}
	if len(out.Attributes) == 0 {
		return nil, ErrNotFound
	}
	var raw dynamoPendingItem
	if err := attributevalue.UnmarshalMap(out.Attributes, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal pending: %w", err)
	}
	return &pendingAuth{
		ClientID:      raw.ClientID,
		RedirectURI:   raw.RedirectURI,
		CodeChallenge: raw.CodeChallenge,
		OriginalState: raw.OriginalState,
		Scope:         raw.Scope,
		Expires:       unixToTime(raw.ExpiresUnix),
	}, nil
}

// --- refresh tokens -------------------------------------------------------

func (d *dynamoStore) PutRefresh(ctx context.Context, rt *refreshToken) error {
	if d.refreshTable == "" {
		// No refresh table configured (e.g. rolled out before the
		// environment PR that provisions it). Signal "not stored" so the
		// caller omits refresh_token instead of handing back one that
		// can never be redeemed.
		return errors.New("refresh table not configured")
	}
	item, err := attributevalue.MarshalMap(dynamoRefreshItem{
		HashedToken:       rt.HashedToken,
		ClientID:          rt.ClientID,
		Scope:             rt.Scope,
		UserSubject:       rt.UserSubject,
		UserEmail:         rt.UserEmail,
		OrganizationIDs:   rt.OrganizationIDs,
		CognitoRefreshEnc: rt.CognitoRefreshEnc,
		ExpiresUnix:       rt.Expires.Unix(),
	})
	if err != nil {
		return fmt.Errorf("marshal refresh: %w", err)
	}
	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.refreshTable),
		Item:      item,
	})
	return err
}

func (d *dynamoStore) PopRefresh(ctx context.Context, hashedToken string) (*refreshToken, error) {
	if d.refreshTable == "" {
		return nil, ErrNotFound
	}
	out, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(d.refreshTable),
		Key: map[string]types.AttributeValue{
			"hashed_token": &types.AttributeValueMemberS{Value: hashedToken},
		},
		ReturnValues: types.ReturnValueAllOld,
	})
	if err != nil {
		return nil, err
	}
	if len(out.Attributes) == 0 {
		return nil, ErrNotFound
	}
	var raw dynamoRefreshItem
	if err := attributevalue.UnmarshalMap(out.Attributes, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal refresh: %w", err)
	}
	return &refreshToken{
		HashedToken:       raw.HashedToken,
		ClientID:          raw.ClientID,
		Scope:             raw.Scope,
		UserSubject:       raw.UserSubject,
		UserEmail:         raw.UserEmail,
		OrganizationIDs:   raw.OrganizationIDs,
		CognitoRefreshEnc: raw.CognitoRefreshEnc,
		Expires:           unixToTime(raw.ExpiresUnix),
	}, nil
}

// --- helpers --------------------------------------------------------------

// errorsAsNotFound returns true if err is a conditional-check or
// item-not-found error from DynamoDB. Kept for future use; today we
// rely on empty Item / Attributes maps for not-found semantics.
func errorsAsNotFound(err error) bool {
	var ccfe *types.ConditionalCheckFailedException
	return errors.As(err, &ccfe)
}
