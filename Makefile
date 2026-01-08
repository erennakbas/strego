.PHONY: proto build test clean

# Generate protobuf code
proto:
	@mkdir -p internal/proto
	protoc --go_out=. --go_opt=paths=source_relative \
		proto/strego.proto

# Build the project
build:
	go build -v ./...

# Run tests
test:
	go test -v -race ./...

# Run tests with coverage
test-coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Clean generated files
clean:
	rm -rf internal/proto/*.pb.go
	rm -f coverage.out coverage.html

# Install dependencies
deps:
	go mod download
	go mod tidy

# Lint code
lint:
	golangci-lint run ./...

# Format code
fmt:
	go fmt ./...
	goimports -w .
