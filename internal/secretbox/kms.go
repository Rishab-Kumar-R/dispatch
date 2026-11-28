package secretbox

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/google/uuid"
)

// KMS encrypts endpoint signing secrets with a customer-managed key. The
// endpoint ID is bound in as the encryption context, so a ciphertext only
// decrypts for the endpoint it was made for.
type KMS struct {
	client *kms.Client
	keyID  string
}

func NewKMS(client *kms.Client, keyID string) *KMS {
	return &KMS{
		client: client,
		keyID:  keyID,
	}
}

func ctxFor(endpointID uuid.UUID) map[string]string {
	return map[string]string{
		"endpoint_id": endpointID.String(),
	}
}

func (k *KMS) Encrypt(ctx context.Context, endpointID uuid.UUID, plaintext string) ([]byte, string, error) {
	out, err := k.client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:             aws.String(k.keyID),
		Plaintext:         []byte(plaintext),
		EncryptionContext: ctxFor(endpointID),
	})
	if err != nil {
		return nil, "", fmt.Errorf("kms encrypt: %w", err)
	}

	return out.CiphertextBlob, aws.ToString(out.KeyId), nil
}

func (k *KMS) Decrypt(ctx context.Context, endpointID uuid.UUID, ciphertext []byte) (string, error) {
	out, err := k.client.Decrypt(ctx, &kms.DecryptInput{
		KeyId:             aws.String(k.keyID),
		CiphertextBlob:    ciphertext,
		EncryptionContext: ctxFor(endpointID),
	})
	if err != nil {
		return "", fmt.Errorf("kms decrypt: %w", err)
	}

	return string(out.Plaintext), nil
}
