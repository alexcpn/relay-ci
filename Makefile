.PHONY: proto build clean test

PROTO_DIR := proto
GEN_DIR := gen

# Generate Go code from proto files
proto:
	@mkdir -p $(GEN_DIR)
	protoc --proto_path=$(PROTO_DIR) \
		--go_out=$(GEN_DIR) --go_opt=paths=source_relative \
		--go-grpc_out=$(GEN_DIR) --go-grpc_opt=paths=source_relative \
		$(PROTO_DIR)/ci/v1/*.proto
	@echo "Proto generation complete"

# Build all binaries
build: proto
	go build -o bin/ci-master ./cmd/master
	go build -o bin/ci-worker ./cmd/worker
	go build -o bin/ci-cli ./cmd/cli

# Run tests
test:
	go test ./...

# Clean generated files and binaries
clean:
	rm -rf $(GEN_DIR) bin/
