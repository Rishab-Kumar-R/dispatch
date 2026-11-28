package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/Rishab-Kumar-R/dispatch/internal/delivery"
	"github.com/Rishab-Kumar-R/dispatch/internal/secretbox"
	"github.com/Rishab-Kumar-R/dispatch/internal/store"
	"github.com/Rishab-Kumar-R/dispatch/internal/worker"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	w, err := build(context.Background(), logger)
	if err != nil {
		logger.Error("init failed", "err", err)
		os.Exit(1)
	}

	lambda.Start(func(ctx context.Context, ev events.SQSEvent) (events.SQSEventResponse, error) {
		recs := make([]worker.Record, len(ev.Records))
		for i, r := range ev.Records {
			recs[i] = worker.Record{
				MessageID: r.MessageId,
				Body:      r.Body,
			}
		}
		failed := w.HandleBatch(ctx, recs)

		resp := events.SQSEventResponse{
			BatchItemFailures: make([]events.SQSBatchItemFailure, len(failed)),
		}
		for i, id := range failed {
			resp.BatchItemFailures[i] = events.SQSBatchItemFailure{
				ItemIdentifier: id,
			}
		}
		return resp, nil
	})
}

func build(ctx context.Context, logger *slog.Logger) (*worker.Worker, error) {
	cfg, err := store.ConfigFromEnv("dispatch-worker")
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
	breaker, err := delivery.NewBreaker(delivery.DefaultBreakerConfig)
	if err != nil {
		return nil, err
	}
	return worker.New(worker.Config{
		Store:   st,
		Secrets: secretbox.NewKMS(kms.NewFromConfig(awsCfg), os.Getenv("KMS_KEY_ID")),
		Sender:  delivery.NewClient(delivery.ClientConfig{}),
		Breaker: breaker,
		Backoff: delivery.DefaultBackoff,
		Logger:  logger,
	}), nil
}
