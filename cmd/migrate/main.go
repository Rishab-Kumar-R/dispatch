package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/url"
	"os"

	"github.com/Rishab-Kumar-R/dispatch/migrations"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

const appRole = "dispatch_app"

type event struct {
	Action string `json:"action"`
}

type dbCreds struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	lambda.Start(handler)
}

func handler(ctx context.Context, ev event) (map[string]any, error) {
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	masterARN, appARN := os.Getenv("DB_SECRET_ARN"), os.Getenv("APP_DB_SECRET_ARN")
	if host == "" || port == "" || name == "" || masterARN == "" || appARN == "" {
		return nil, errors.New("missing env: DB_HOST, DB_PORT, DB_NAME, DB_SECRET_ARN, APP_DB_SECRET_ARN")
	}

	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	sm := secretsmanager.NewFromConfig(awsCfg)

	master, err := getCreds(ctx, sm, masterARN)
	if err != nil {
		return nil, fmt.Errorf("read master secret: %w", err)
	}

	db, err := openDB(host, port, name, master)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = db.Close()
	}()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("session locker: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("goose provider: %w", err)
	}

	if ev.Action == "status" {
		st, err := provider.Status(ctx)
		if err != nil {
			return nil, fmt.Errorf("status: %w", err)
		}
		out := make([]map[string]any, 0, len(st))
		for _, s := range st {
			out = append(out, map[string]any{"version": s.Source.Version, "state": string(s.State)})
		}
		return map[string]any{"migrations": out}, nil
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrate up: %w", err)
	}
	applied := make([]int64, 0, len(results))
	for _, r := range results {
		applied = append(applied, r.Source.Version)
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("db version: %w", err)
	}

	app, err := ensureAppCreds(ctx, sm, appARN)
	if err != nil {
		return nil, fmt.Errorf("ensure app secret: %w", err)
	}
	if err := setupAppRole(ctx, db, name, app); err != nil {
		return nil, fmt.Errorf("set up app role: %w", err)
	}

	slog.InfoContext(ctx, "migrate done", "applied", applied, "db_version", version)
	return map[string]any{"applied": applied, "db_version": version, "app_role": appRole}, nil
}

func getCreds(ctx context.Context, sm *secretsmanager.Client, arn string) (*dbCreds, error) {
	out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(arn)})
	if err != nil {
		return nil, err
	}
	var c dbCreds
	if err := json.Unmarshal([]byte(aws.ToString(out.SecretString)), &c); err != nil {
		return nil, fmt.Errorf("parse secret json: %w", err)
	}

	return &c, nil
}

func ensureAppCreds(ctx context.Context, sm *secretsmanager.Client, arn string) (*dbCreds, error) {
	c, err := getCreds(ctx, sm, arn)
	if err == nil && c.Password != "" {
		return c, nil
	}
	var notFound *types.ResourceNotFoundException
	if err != nil && !errors.As(err, &notFound) {
		return nil, err
	}

	pw, err := randomPassword(32)
	if err != nil {
		return nil, err
	}
	c = &dbCreds{
		Username: appRole,
		Password: pw,
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	if _, err := sm.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     aws.String(arn),
		SecretString: aws.String(string(b)),
	}); err != nil {
		return nil, fmt.Errorf("store app secret: %w", err)
	}

	return c, nil
}

func setupAppRole(ctx context.Context, db *sql.DB, dbName string, c *dbCreds) error {
	var exists bool
	if err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, c.Username).Scan(&exists); err != nil {
		return err
	}

	steps := make([]step, 0, 9)
	if !exists {
		steps = append(steps, step{"CREATE ROLE %I LOGIN", []string{c.Username}})
	}

	steps = append(steps,
		step{"ALTER ROLE %I WITH LOGIN PASSWORD %L", []string{c.Username, c.Password}},
		step{"ALTER ROLE %I SET statement_timeout = '15s'", []string{c.Username}},
		step{"ALTER ROLE %I SET idle_in_transaction_session_timeout = '30s'", []string{c.Username}},
		step{"GRANT CONNECT ON DATABASE %I TO %I", []string{dbName, c.Username}},
		step{"GRANT USAGE ON SCHEMA public TO %I", []string{c.Username}},
		step{"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %I", []string{c.Username}},
		step{"REVOKE ALL ON TABLE public.goose_db_version FROM %I", []string{c.Username}},
		step{"ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I", []string{c.Username}},
	)

	for _, s := range steps {
		if err := execFormatted(ctx, db, s.tmpl, s.args...); err != nil {
			return fmt.Errorf("%q: %w", s.tmpl, err)
		}
	}

	return nil
}

type step struct {
	tmpl string
	args []string
}

// execFormatted lets Postgres quote identifiers (%I) and literals (%L) itself,
// so no SQL is built by string concatenation here (utility statements can't be parameterised).
func execFormatted(ctx context.Context, db *sql.DB, tmpl string, args ...string) error {
	params := make([]any, 0, len(args)+1)
	params = append(params, tmpl)
	q := "SELECT format($1::text"

	for i, a := range args {
		params = append(params, a)
		q += fmt.Sprintf(", $%d::text", i+2)
	}
	q += ")"

	var stmt string
	if err := db.QueryRowContext(ctx, q, params...).Scan(&stmt); err != nil {
		return err
	}

	_, err := db.ExecContext(ctx, stmt)
	return err
}

func openDB(host, port, name string, c *dbCreds) (*sql.DB, error) {
	dsn := fmt.Sprintf("postgres://%s@%s:%s/%s?sslmode=require&connect_timeout=10", url.UserPassword(c.Username, c.Password).String(), host, port, name)
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}

	return stdlib.OpenDB(*cfg), nil
}

const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func randomPassword(n int) (string, error) {
	b := make([]byte, n)
	maxi := big.NewInt(int64(len(alphabet)))

	for i := range b {
		v, err := rand.Int(rand.Reader, maxi)
		if err != nil {
			return "", err
		}

		b[i] = alphabet[v.Int64()]
	}

	return string(b), nil
}
