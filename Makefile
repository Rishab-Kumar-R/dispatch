SHELL := /bin/bash
.DEFAULT_GOAL := help

FUNCS      := ingest relay worker migrate
DIST       := dist
ENV        ?= dev
TF_DIR     := infra/envs/$(ENV)
AWS_REGION ?= ap-south-1
GOFLAGS    := -trimpath
TUNNEL_PORT ?= 5433

.PHONY: help build placeholders test vet lint fmt tidy clean check \
        sqlc sqlc-check migrate-up migrate-down migrate-status migrate-new \
        tf-init tf-fmt tf-validate tf-plan tf-apply tf-destroy tunnel psql

help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*## "}{printf "  %-16s %s\n", $$1, $$2}'

build: ## Build all Lambdas (linux/arm64) into dist/*.zip
	@mkdir -p $(DIST)
	@for f in $(FUNCS); do \
		echo "==> $$f"; \
		rm -rf $(DIST)/$$f && mkdir -p $(DIST)/$$f && \
		GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build $(GOFLAGS) -tags lambda.norpc \
			-ldflags="-s -w" -o $(DIST)/$$f/bootstrap ./cmd/$$f && \
		(cd $(DIST)/$$f && zip -qj ../$$f.zip bootstrap) || exit 1; \
	done

test: ## Unit tests with the race detector
	go test -race -count=1 ./...

vet: ## go vet
	go vet ./...

lint: ## golangci-lint (brew install golangci-lint)
	golangci-lint run ./...

fmt: ## Format Go code
	gofmt -s -w .

tidy: ## Tidy go.mod
	go mod tidy

clean: ## Remove build output
	rm -rf $(DIST)

check: fmt vet test sqlc-check ## Everything CI will run (locally)

sqlc: ## Generate type-safe Go from SQL
	sqlc generate

sqlc-check: ## Fail if generated code is stale
	sqlc diff

migrate-up: ## goose up (needs DB_URL)
	@test -n "$(DB_URL)" || { echo "set DB_URL (open 'make tunnel' first)"; exit 1; }
	goose -dir migrations postgres "$(DB_URL)" up

migrate-down: ## goose down, one step (needs DB_URL)
	@test -n "$(DB_URL)" || { echo "set DB_URL (open 'make tunnel' first)"; exit 1; }
	goose -dir migrations postgres "$(DB_URL)" down

migrate-status: ## goose status (needs DB_URL)
	@test -n "$(DB_URL)" || { echo "set DB_URL (open 'make tunnel' first)"; exit 1; }
	goose -dir migrations postgres "$(DB_URL)" status

migrate-new: ## Create a migration: make migrate-new name=add_something
	@test -n "$(name)" || { echo "usage: make migrate-new name=add_something"; exit 1; }
	goose -dir migrations create -s $(name) sql

tf-init: ## terraform init
	terraform -chdir=$(TF_DIR) init -backend-config=backend.hcl

tf-fmt: ## Format all Terraform
	terraform fmt -recursive infra

tf-validate: ## Validate the environment
	terraform -chdir=$(TF_DIR) validate

tf-plan: build ## Build, then plan (saves tfplan)
	terraform -chdir=$(TF_DIR) plan -out=tfplan

tf-apply: ## Apply the saved plan
	terraform -chdir=$(TF_DIR) apply tfplan

tf-destroy: ## Destroy the environment (asks for confirmation)
	terraform -chdir=$(TF_DIR) destroy

tunnel: ## Port-forward RDS to localhost:$(TUNNEL_PORT)
	aws ssm start-session --region $(AWS_REGION) \
	  --target "$$(terraform -chdir=$(TF_DIR) output -raw bastion_instance_id)" \
	  --document-name AWS-StartPortForwardingSessionToRemoteHost \
	  --parameters "host=$$(terraform -chdir=$(TF_DIR) output -raw db_address),portNumber=5432,localPortNumber=$(TUNNEL_PORT)"

psql: ## psql through the tunnel
	@PGPASSWORD="$$(aws secretsmanager get-secret-value --region $(AWS_REGION) \
	  --secret-id "$$(terraform -chdir=$(TF_DIR) output -raw db_secret_arn)" \
	  --query SecretString --output text | jq -r .password)" \
	psql "host=localhost port=$(TUNNEL_PORT) dbname=dispatchdb user=dbadmin sslmode=require"
