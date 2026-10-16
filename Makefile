.PHONY: all server client client-windows test clean

BIN_DIR := bin

all: server client

server:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN_DIR)/p2ptunnel-server ./cmd/server

client:
	@mkdir -p $(BIN_DIR)
	go build -trimpath -o $(BIN_DIR)/p2ptunnel ./cmd/client

# Cross-compile for Windows from Linux (requires mingw-w64 gcc: x86_64-w64-mingw32-gcc)
client-windows:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc \
		go build -trimpath -ldflags="-s -w -H=windowsgui" -o $(BIN_DIR)/p2ptunnel.exe ./cmd/client

test:
	go test -v -race ./internal/...

clean:
	rm -rf $(BIN_DIR)
