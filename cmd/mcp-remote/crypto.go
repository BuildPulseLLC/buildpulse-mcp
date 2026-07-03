package main

// Application-layer encryption for the one high-value secret this server
// persists: the upstream Cognito refresh token.
//
// Every MCP-issued token (access + refresh) is only ever stored HASHED —
// we never need to recover it. The Cognito refresh token is different: we
// must be able to replay it against Cognito's refresh grant to re-mint
// sessions (and to re-check that the user is still valid). So it is stored
// reversibly, encrypted with a KMS CMK at the application layer. DynamoDB's
// own at-rest SSE sits underneath this as defense-in-depth; the app-layer
// CMK means a raw table read alone can't reuse the credential.

import (
	"context"
	"encoding/base64"
	"errors"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

const envOAuthKMSKeyARN = "OAUTH_KMS_KEY_ARN"

// crypter reversibly protects a small secret. Ciphertext is returned as a
// base64 string so it stores cleanly as a DynamoDB string attribute.
type crypter interface {
	Encrypt(ctx context.Context, plaintext string) (string, error)
	Decrypt(ctx context.Context, ciphertext string) (string, error)
}

// kmsCrypter uses AWS KMS Encrypt/Decrypt directly. A Cognito refresh
// token is well under the 4KB KMS plaintext limit, so no envelope
// (GenerateDataKey) is needed — one KMS call per encrypt/decrypt.
type kmsCrypter struct {
	client *kms.Client
	keyID  string
}

func (k *kmsCrypter) Encrypt(ctx context.Context, plaintext string) (string, error) {
	out, err := k.client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     aws.String(k.keyID),
		Plaintext: []byte(plaintext),
	})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(out.CiphertextBlob), nil
}

func (k *kmsCrypter) Decrypt(ctx context.Context, ciphertext string) (string, error) {
	blob, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	out, err := k.client.Decrypt(ctx, &kms.DecryptInput{
		KeyId:          aws.String(k.keyID),
		CiphertextBlob: blob,
	})
	if err != nil {
		return "", err
	}
	return string(out.Plaintext), nil
}

// plaintextCrypter is the local/dev fallback used when OAUTH_KMS_KEY_ARN
// is unset (no KMS reachable). It base64-wraps the value so the stored
// shape matches the KMS path, but performs NO real encryption. It is
// never selected in production, where the env var is always injected.
type plaintextCrypter struct{}

func (plaintextCrypter) Encrypt(_ context.Context, plaintext string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(plaintext)), nil
}

func (plaintextCrypter) Decrypt(_ context.Context, ciphertext string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// erroringCrypter is returned when a KMS key IS configured but the AWS
// client could not be built. It fails every call — crucially it does NOT
// silently fall back to plaintext, so a misconfigured prod deploy simply
// won't issue refresh tokens (access tokens still work) rather than
// storing a high-value credential in the clear.
type erroringCrypter struct{ err error }

func (e erroringCrypter) Encrypt(context.Context, string) (string, error) {
	return "", e.err
}

func (e erroringCrypter) Decrypt(context.Context, string) (string, error) {
	return "", e.err
}

// buildCrypter selects the crypter from the environment, mirroring
// buildOAuthStore's degrade-in-dev philosophy:
//
//   - OAUTH_KMS_KEY_ARN set   → KMS-backed (production).
//   - OAUTH_KMS_KEY_ARN unset → plaintext passthrough (local/dev only).
//
// When the key IS set but AWS config fails to load we return an
// erroringCrypter, never plaintext — see the type doc above.
func buildCrypter(ctx context.Context) crypter {
	keyARN := os.Getenv(envOAuthKMSKeyARN)
	if keyARN == "" {
		log.Printf("refresh-token crypter: OAUTH_KMS_KEY_ARN unset — upstream refresh tokens stored WITHOUT encryption (dev only)")
		return plaintextCrypter{}
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		log.Printf("refresh-token crypter: OAUTH_KMS_KEY_ARN set but AWS config load failed (%v) — refresh tokens DISABLED (will not store plaintext)", err)
		return erroringCrypter{err: errors.New("kms crypter unavailable: " + err.Error())}
	}
	log.Printf("refresh-token crypter: using KMS (key=%s)", keyARN)
	return &kmsCrypter{client: kms.NewFromConfig(cfg), keyID: keyARN}
}
