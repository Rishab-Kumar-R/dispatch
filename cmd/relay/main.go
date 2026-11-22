package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	lambda.Start(func(ctx context.Context, in map[string]any) (map[string]any, error) {
		slog.InfoContext(ctx, "stub invoked", "fn", "relay")
		return map[string]any{"ok": true}, nil
	})
}
