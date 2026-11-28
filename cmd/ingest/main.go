package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/Rishab-Kumar-R/dispatch/internal/api"
	"github.com/Rishab-Kumar-R/dispatch/internal/queue"
	"github.com/Rishab-Kumar-R/dispatch/internal/secretbox"
	"github.com/Rishab-Kumar-R/dispatch/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	h, err := build(context.Background(), logger)
	if err != nil {
		logger.Error("init failed", "err", err)
		os.Exit(1)
	}

	lambda.Start(httpadapter.NewV2(h).ProxyWithContext)
}

func build(ctx context.Context, logger *slog.Logger) (http.Handler, error) {
	cfg, err := store.ConfigFromEnv("dispatch-ingest")
	if err != nil {
		return nil, err
	}
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	src := store.NewSecretsManagerSource(secretsmanager.NewFromConfig(awsCfg), cfg.SecretARN, 5*time.Minute)
	st, err := store.Open(ctx, cfg, src)
	if err != nil {
		return nil, err
	}

	return api.New(api.Deps{
		Store:     st,
		Publisher: queue.NewSQS(sqs.NewFromConfig(awsCfg), os.Getenv("QUEUE_URL")),
		Secrets:   secretbox.NewKMS(kms.NewFromConfig(awsCfg), os.Getenv("KMS_KEY_ID")),
		Logger:    logger,
	}), nil
}
