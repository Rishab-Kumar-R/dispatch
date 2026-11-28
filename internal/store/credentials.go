package store

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// CredentialsSource supplies database credentials on every new connection
type CredentialsSource interface {
	Credentials(ctx context.Context) (user, password string, err error)
}

type SecretsManagerSource struct {
	client *secretsmanager.Client
	arn    string
	ttl    time.Duration

	mu      sync.Mutex
	user    string
	pass    string
	fetched time.Time
}

func NewSecretsManagerSource(client *secretsmanager.Client, arn string, ttl time.Duration) *SecretsManagerSource {
	return &SecretsManagerSource{
		client: client,
		arn:    arn,
		ttl:    ttl,
	}
}

func (s *SecretsManagerSource) Credentials(ctx context.Context) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.user != "" && time.Since(s.fetched) < s.ttl {
		return s.user, s.pass, nil
	}
	out, err := s.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(s.arn),
	})
	if err != nil {
		return "", "", fmt.Errorf("read db secret: %w", err)
	}
	var c struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(aws.ToString(out.SecretString)), &c); err != nil {
		return "", "", fmt.Errorf("parse db secret: %w", err)
	}
	if c.Username == "" || c.Password == "" {
		return "", "", errors.New("db secret has no username/password yet (has the migrate Lambda run?)")
	}

	s.user, s.pass, s.fetched = c.Username, c.Password, time.Now()
	return s.user, s.pass, nil
}
