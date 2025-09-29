gen-node:
	@which protoc > /dev/null || (echo "protoc not found. Installing via Homebrew..." && (which brew >/dev/null && brew install protobuf) || (echo "Homebrew not available. Please install protoc manually: https://grpc.io/docs/protoc-installation/" && exit 1))
	@which protoc-gen-ts > /dev/null || (echo "protoc-gen-ts not found. Installing..." && bun install -g protoc-gen-ts)
	mkdir -p generated/node/proto
	protoc --proto_path=. --ts_out=generated/node/proto --ts_opt=paths=source_relative ./jobs.proto

generate:
	make gen-node

gen-go:
	@which protoc > /dev/null || (echo "protoc not found. Installing via Homebrew..." && (which brew >/dev/null && brew install protobuf) || (echo "Homebrew not available. Please install protoc manually: https://grpc.io/docs/protoc-installation/" && exit 1))
	@which protoc-gen-go > /dev/null || (echo "protoc-gen-go not found. Installing..." && go install google.golang.org/protobuf/cmd/protoc-gen-go@latest)
	@which protoc-gen-go-grpc > /dev/null || (echo "protoc-gen-go-grpc not found. Installing..." && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest)
	PATH="$(shell go env GOPATH)/bin:$$PATH" protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative ./jobs.proto


