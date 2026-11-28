package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Rishab-Kumar-R/dispatch/internal/auth"
	"github.com/Rishab-Kumar-R/dispatch/internal/store"
	"github.com/Rishab-Kumar-R/dispatch/internal/store/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const usage = `usage: DISPATCH_DSN=... go run ./cmd/admin <command>
  create-tenant --name NAME [--rate-limit 600]   creates a tenant and its first API key
  create-key    --tenant UUID [--name NAME]      issues another API key
  delete-tenant --id UUID                        deletes a tenant and ALL its data`

func main() {
	if len(os.Args) < 2 {
		_, err := fmt.Fprintln(os.Stderr, usage)
		if err != nil {
			return
		}

		os.Exit(2)
	}
	if err := run(context.Background(), os.Args[1], os.Args[2:]); err != nil {
		_, err := fmt.Fprintln(os.Stderr, "error:", err)
		if err != nil {
			return
		}

		os.Exit(1)
	}
}

func run(ctx context.Context, cmd string, args []string) error {
	dsn := os.Getenv("DISPATCH_DSN")
	if dsn == "" {
		return fmt.Errorf("DISPATCH_DSN is not set (open `make tunnel` first)")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	st := store.New(pool)

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	switch cmd {
	case "create-tenant":
		name := fs.String("name", "", "tenant name")
		rate := fs.Int("rate-limit", 600, "requests per minute")
		_ = fs.Parse(args)
		if *name == "" {
			return fmt.Errorf("--name is required")
		}
		return st.InTx(ctx, func(q *db.Queries) error {
			t, err := q.CreateTenant(ctx, db.CreateTenantParams{
				ID: store.NewID(), Name: *name, RateLimitPerMinute: int32(*rate),
			})
			if err != nil {
				return err
			}
			fmt.Println("tenant:", t.ID)
			return issueKey(ctx, q, t.ID, "default")
		})
	case "create-key":
		tenant := fs.String("tenant", "", "tenant id")
		name := fs.String("name", "key", "key name")
		_ = fs.Parse(args)
		id, err := uuid.Parse(*tenant)
		if err != nil {
			return fmt.Errorf("--tenant must be a uuid")
		}
		return issueKey(ctx, st.Queries(), id, *name)
	case "delete-tenant":
		id := fs.String("id", "", "tenant id")
		_ = fs.Parse(args)
		tid, err := uuid.Parse(*id)
		if err != nil {
			return fmt.Errorf("--id must be a uuid")
		}
		tag, err := pool.Exec(ctx, `DELETE FROM tenants WHERE id = $1`, tid)
		if err != nil {
			return err
		}
		fmt.Println("deleted tenants:", tag.RowsAffected())
		return nil
	}

	return fmt.Errorf("unknown command %q\n%s", cmd, usage)
}

func issueKey(ctx context.Context, q *db.Queries, tenant uuid.UUID, name string) error {
	raw, prefix, hash, err := auth.GenerateKey()
	if err != nil {
		return err
	}
	if _, err := q.CreateAPIKey(ctx, db.CreateAPIKeyParams{
		ID: store.NewID(), TenantID: tenant, Name: name, KeyPrefix: prefix, KeyHash: hash,
	}); err != nil {
		return err
	}
	fmt.Println("api key (shown once, store it now):", raw)
	return nil
}
