.PHONY: build run test lint telemetry

build:
	go build -o bin/resolver ./cmd/resolver
	go build -o bin/stubupstream ./cmd/stubupstream
	go build -o bin/loadtest ./cmd/loadtest

run: build
	./bin/resolver

test:
	go test -race ./...

lint:
	golangci-lint run

# Generate a labeled training dataset: run the resolver against the stub under
# load with telemetry on, then derive the look-ahead label offline.
telemetry: build
	./scripts/gen_telemetry.sh
