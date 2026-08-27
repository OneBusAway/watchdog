VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags="-X main.version=$(VERSION)"

.PHONY: compile docker fmt fmt-check test vet

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

# The integration tests are behind a build tag and never run in CI, so nothing
# else type-checks them and they rot silently. The tagged run is a superset of
# the untagged one for as long as no file carries a !integration constraint --
# add one and this stops covering it.
vet:
	go vet -tags=integration ./...