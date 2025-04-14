compile:
	CGO_ENABLED=0 go build -v -o . ./...

docker:
	docker build -t watchdog .

fmt:
	gofmt -w .

test:
	go test ./...

vet:
	go vet ./...
