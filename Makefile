SHELL := /usr/bin/env bash
GO ?= go
FUZZTIME ?= 5s
COVERAGE_PROFILE ?= coverage.out
COVERAGE_HTML ?= coverage.html

.PHONY: fmt fmt-check generate-check vet test race race-build race-kernel-source race-runtime race-commerce \
	fuzz fuzz-kernel fuzz-source fuzz-build fuzz-runtime fuzz-commerce coverage coverage-html \
	integration-compile postgres-up postgres-down postgres-reset test-postgres test-postgres-docker test-runtime-postgres test-commerce-postgres build \
	smoke-runtime-api smoke-commerce-api tdd-iteration4 tdd-iteration6 manifests-iteration4 \
	verify verify-iteration3 verify-iteration4 verify-iteration6 clean

fmt:
	@gofmt -w $$(find . -name '*.go' -type f -not -path './vendor/*' | sort)

fmt-check:
	@files="$$(gofmt -l $$(find . -name '*.go' -type f -not -path './vendor/*' | sort))"; \
	if [[ -n "$$files" ]]; then echo "Unformatted Go files:"; echo "$$files"; exit 1; fi

generate-check:
	@echo "No generated Go code is committed in iterations 1-4 and 6"

vet:
	$(GO) vet ./...
	CGO_ENABLED=1 $(GO) vet -tags=postgres_integration ./test/integration
	$(GO) vet -tags=integration_postgres ./internal/source/postgres

test:
	$(GO) test -count=1 ./...

race: race-build race-kernel-source race-runtime race-commerce

race-build:
	$(GO) test -race -count=1 ./internal/build/... ./pkg/contracts/build/v1 ./test/acceptance ./test/architecture

race-kernel-source:
	$(GO) test -race -count=1 ./internal/kernel/... ./adapters/... ./contracts/... ./internal/source/... ./pkg/contracts/source/v1 ./test/contract

race-runtime:
	$(GO) test -race -count=1 ./internal/runtime/... ./pkg/contracts/runtime/v1 ./test/contract ./test/architecture
	$(GO) test -race -count=1 ./test/acceptance -run '^TestRuntimeDelivery_'

race-commerce:
	$(GO) test -race -count=1 ./internal/commerce/... ./pkg/contracts/commerce/v1 ./test/acceptance ./test/contract ./test/architecture

fuzz: fuzz-kernel fuzz-source fuzz-build fuzz-runtime fuzz-commerce

fuzz-kernel:
	$(GO) test ./internal/kernel -run='^$$' -fuzz=FuzzOperationTransitionNeverMutatesOnRejectedTransition -fuzztime=$(FUZZTIME)

fuzz-source:
	$(GO) test ./internal/source/workspace -run='^$$' -fuzz=FuzzPatchPathCannotEscape -fuzztime=$(FUZZTIME)

fuzz-build:
	$(GO) test ./internal/build/domain -run='^$$' -fuzz=FuzzBuildConfig_PathNormalizationCannotEscape -fuzztime=$(FUZZTIME)

fuzz-runtime:
	$(GO) test ./pkg/contracts/runtime/v1 -run='^$$' -fuzz=FuzzPaaSAppValidateNeverPanics -fuzztime=$(FUZZTIME)
	$(GO) test ./internal/runtime/gitops -run='^$$' -fuzz=FuzzGitOpsPathValidationCannotEscape -fuzztime=$(FUZZTIME)
	$(GO) test ./internal/runtime/domain -run='^$$' -fuzz=FuzzRuntimeObjectNamesRemainDNSBounded -fuzztime=$(FUZZTIME)

fuzz-commerce:
	$(GO) test ./internal/commerce/application -run='^$$' -fuzz=FuzzRuntimeUsageNoPanic -fuzztime=$(FUZZTIME)
	$(GO) test ./internal/commerce/application -run='^$$' -fuzz=FuzzRateQuantityNoPanic -fuzztime=$(FUZZTIME)

coverage:
	$(GO) test -count=1 -covermode=atomic -coverpkg=./... -coverprofile=$(COVERAGE_PROFILE) ./...
	$(GO) tool cover -func=$(COVERAGE_PROFILE) | tail -1

coverage-html: coverage
	$(GO) tool cover -html=$(COVERAGE_PROFILE) -o $(COVERAGE_HTML)

integration-compile:
	CGO_ENABLED=1 $(GO) test -count=1 -tags=postgres_integration ./test/integration -run='^$$'
	$(GO) test -count=1 -tags=integration_postgres ./internal/source/postgres -run='^$$'

postgres-up:
	docker compose -f compose.test.yaml up -d --wait postgres

postgres-down:
	docker compose -f compose.test.yaml down

postgres-reset:
	docker compose -f compose.test.yaml down --volumes

test-postgres-docker:
	./scripts/test-postgres-docker.sh

test-postgres:
	@test -n "$$TEST_POSTGRES_DSN" || (echo "TEST_POSTGRES_DSN is required" >&2; exit 2)
	CGO_ENABLED=1 $(GO) test -race -count=1 -tags=postgres_integration ./test/integration -v
	$(GO) test -race -count=1 -tags=integration_postgres ./internal/source/postgres -v

test-runtime-postgres:
	@test -n "$$TEST_POSTGRES_DSN" || (echo "TEST_POSTGRES_DSN is required" >&2; exit 2)
	CGO_ENABLED=1 $(GO) test -race -count=1 -tags=postgres_integration ./test/integration -run '^TestPostgres_Runtime' -v

test-commerce-postgres:
	@test -n "$$TEST_POSTGRES_DSN" || (echo "TEST_POSTGRES_DSN is required" >&2; exit 2)
	CGO_ENABLED=1 $(GO) test -race -count=1 -tags=postgres_integration ./test/integration -run '^TestPostgres_Commerce' -v

build:
	mkdir -p bin
	$(GO) build -trimpath -o bin/kernel-api ./cmd/kernel-api
	$(GO) build -trimpath -o bin/source-api ./cmd/source-api
	$(GO) build -trimpath -o bin/build-api ./cmd/build-api
	$(GO) build -trimpath -o bin/runtime-api ./cmd/runtime-api
	$(GO) build -trimpath -o bin/runtime-operator ./cmd/runtime-operator
	$(GO) build -trimpath -o bin/commerce-api ./cmd/commerce-api

smoke-runtime-api: build
	./scripts/runtime-api-smoke.sh

smoke-commerce-api: build
	./scripts/commerce-api-smoke.sh

tdd-iteration4:
	./scripts/verify-iteration-4-tdd.py --matrix docs/iteration-4/TDD_MATRIX.md

tdd-iteration6:
	./scripts/verify-iteration-6-tdd.py

manifests-iteration4:
	$(GO) test -count=1 ./test/contract -run 'Test(Argo|Delete_Argo|PaaSAppCRD|RuntimeOperatorRBAC)' -v

verify: verify-iteration6

verify-iteration3:
	./scripts/verify-iteration-3.sh

verify-iteration4:
	./scripts/verify-iteration-4.sh

verify-iteration6:
	./scripts/verify-iteration-6.sh

clean:
	rm -rf bin $(COVERAGE_PROFILE) $(COVERAGE_HTML) .verification/iteration-3 .verification/iteration-4 .verification/iteration-6 coverage-commerce.html
