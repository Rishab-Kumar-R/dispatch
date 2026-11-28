package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/Rishab-Kumar-R/dispatch/internal/queue"
	"github.com/Rishab-Kumar-R/dispatch/internal/store"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
)

const (
	batchSize  = 100
	maxBatches = 20
	grace      = 30 * time.Second
	margin     = 10 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	ctx := context.Background()

	cfg, err := store.ConfigFromEnv("dispatch-relay")
	if err != nil {
		logger.Error("init failed", "err", err)
		os.Exit(1)
	}
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		logger.Error("init failed", "err", err)
		os.Exit(1)
	}
	src := store.NewSecretsManagerSource(secretsmanager.NewFromConfig(awsCfg), cfg.SecretARN, 5*time.Minute)
	st, err := store.Open(ctx, cfg, src)
	if err != nil {
		logger.Error("init failed", "err", err)
		os.Exit(1)
	}
	pub := queue.NewSQS(sqs.NewFromConfig(awsCfg), os.Getenv("QUEUE_URL"))

	lambda.Start(func(ctx context.Context, _ json.RawMessage) (map[string]int, error) {
		total := 0
		for range maxBatches {
			if dl, ok := ctx.Deadline(); ok && time.Until(dl) < margin {
				break
			}

			n, err := st.PublishPending(ctx, time.Now().Add(-grace), batchSize,
				func(ctx context.Context, batch []store.PendingDelivery) error {
					ids := make([]uuid.UUID, len(batch))
					for i, d := range batch {
						ids[i] = d.ID
					}

					return pub.Publish(ctx, ids)
				})
			if err != nil {
				logger.ErrorContext(ctx, "relay sweep failed", "err", err, "published_so_far", total)
				return nil, err
			}
			total += n
			if n < batchSize {
				break
			}
		}

		logger.InfoContext(ctx, "relay sweep done", "published", total)
		return map[string]int{"published": total}, nil
	})
}
