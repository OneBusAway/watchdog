VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags="-X main.version=$(VERSION)"

.PHONY: compile docker fmt fmt-check sqlc sqlc-check test test-db vet

# sqlc is run through `go run` at a pinned version so every developer and CI
# generate identical code without a separate install. It needs cgo (the
# PostgreSQL parser is C), which the default Go toolchain provides.
SQLC_VERSION ?= v1.31.1
SQLC := go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

compile:
	CGO_ENABLED=0 go build -v $(LDFLAGS) -o . ./...

docker:
	docker build --build-arg VERSION=$(VERSION) -t watchdog .

fmt:
	gofmt -w .

# Reports unformatted files instead of rewriting them. CI runs this target, so
# the check has one definition a developer can also run locally.
#
# gofmt writes parse errors to stderr and exits non-zero, so both streams and
# the exit status have to be checked: capturing stdout alone lets a .go file
# that gofmt cannot parse pass silently. That is not hypothetical for files
# under testdata/, which gofmt walks but go build and go vet do not.
fmt-check:
	@out=$$(gofmt -l . 2>&1); status=$$?; \
	if [ $$status -ne 0 ]; then \
		echo "gofmt failed (exit $$status):"; \
		echo "$$out"; \
		exit $$status; \
	fi; \
	if [ -n "$$out" ]; then \
		echo "The following files need 'make fmt':"; \
		echo "$$out"; \
		exit 1; \
	fi

test:
	go test ./...

# Runs the PostgreSQL tests in internal/db, which skip unless
# WATCHDOG_TEST_DATABASE_URL points at a database they may create schemas in.
# Each test migrates its own temporary schema and drops it afterwards.
test-db:
	@if [ -z "$$WATCHDOG_TEST_DATABASE_URL" ]; then \
		echo "Set WATCHDOG_TEST_DATABASE_URL, e.g. postgres://watchdog:watchdog@localhost:5432/watchdog?sslmode=disable"; \
		exit 1; \
	fi
	go test -count=1 ./internal/db/...

# Regenerates internal/db/dbgen from internal/db/queries and the migrations.
sqlc:
	$(SQLC) generate

# Fails if internal/db/dbgen is out of date. CI runs this target.
sqlc-check:
	$(SQLC) diff

# The integration tests are behind a build tag and never run in CI, so nothing
# else type-checks them and they rot silently. The tagged run is a superset of
# the untagged one for as long as no file carries a !integration constraint --
# add one and this stops covering it.
vet:
	go vet -tags=integration ./...