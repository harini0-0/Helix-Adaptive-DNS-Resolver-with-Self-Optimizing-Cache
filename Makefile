.PHONY: build run test lint telemetry venv train

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

# Create the Python virtualenv for the ML pipeline.
venv:
	python3 -m venv .venv
	.venv/bin/python -m pip install -q --upgrade pip
	.venv/bin/python -m pip install -q -r ml/requirements.txt

# Train + evaluate the retention model and export it for the Go resolver.
# Requires `make telemetry` first (for ml/data/labeled.csv) and `make venv`.
train:
	.venv/bin/python ml/train.py
