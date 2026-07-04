package main

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
)

// These tests exercise the dynamodbav (de)serialization directly, without
// a live DynamoDB — the gap that let a dropped field ship: the in-memory
// Store round-trips whole structs by pointer, so memory-backed tests can't
// catch a field that's missing from the dynamo item shape. In prod the
// store is DynamoDB, where a missing `dynamodbav` field is silently lost.

func TestDynamoCodeItemPreservesCognitoRefreshEnc(t *testing.T) {
	in := dynamoCodeItem{
		Code:              "abc",
		ClientID:          "mcp_test",
		CodeChallenge:     "chal",
		Scope:             defaultScope,
		UserSubject:       "sub-1",
		UserEmail:         "u@e.com",
		OrganizationIDs:   []string{"org-1"},
		CognitoRefreshEnc: "ENCRYPTED-COGNITO-REFRESH",
		ExpiresUnix:       1234567890,
	}
	m, err := attributevalue.MarshalMap(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// The bug: the field never made it into the marshaled item at all.
	if _, ok := m["cognito_refresh_enc"]; !ok {
		t.Fatalf("cognito_refresh_enc missing from marshaled code item: %v", m)
	}
	var out dynamoCodeItem
	if err := attributevalue.UnmarshalMap(m, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.CognitoRefreshEnc != in.CognitoRefreshEnc {
		t.Errorf("CognitoRefreshEnc = %q, want %q", out.CognitoRefreshEnc, in.CognitoRefreshEnc)
	}
}

func TestDynamoRefreshItemPreservesCognitoRefreshEnc(t *testing.T) {
	in := dynamoRefreshItem{
		HashedToken:       "hash",
		ClientID:          "mcp_test",
		Scope:             defaultScope,
		UserSubject:       "sub-1",
		UserEmail:         "u@e.com",
		OrganizationIDs:   []string{"org-1"},
		CognitoRefreshEnc: "ENCRYPTED-COGNITO-REFRESH",
		ExpiresUnix:       1234567890,
	}
	m, err := attributevalue.MarshalMap(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out dynamoRefreshItem
	if err := attributevalue.UnmarshalMap(m, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.CognitoRefreshEnc != in.CognitoRefreshEnc {
		t.Errorf("CognitoRefreshEnc = %q, want %q", out.CognitoRefreshEnc, in.CognitoRefreshEnc)
	}
	if out.HashedToken != in.HashedToken {
		t.Errorf("HashedToken = %q, want %q", out.HashedToken, in.HashedToken)
	}
}
