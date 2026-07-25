VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags="-X main.version=$(VERSION)"

compile:
	CGO_ENABLED=0 go build -v $(LDFLAGS) -o . ./...

docker:
	docker build --build-arg VERSION=$(VERSION) -t watchdog .

fmt:
	gofmt -w .

test:
	go test ./...

vet:
	go vet ./...