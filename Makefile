MAKEFILE_DIR:=$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))

.PHONY: build build-linux build-windows vet test clean run dev

build:
	mkdir -p $(MAKEFILE_DIR)/server/bin
	cd server && CGO_ENABLED=0 go build -o ./bin/tma1-server ./cmd/tma1-server

build-linux:
	mkdir -p $(MAKEFILE_DIR)/server/bin
	cd server && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ./bin/tma1-server ./cmd/tma1-server

build-windows:
	mkdir -p $(MAKEFILE_DIR)/server/bin
	cd server && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o ./bin/tma1-server.exe ./cmd/tma1-server

vet:
	cd server && go vet ./...

test:
	cd server && go test -race -count=1 ./...

clean:
	rm -f server/bin/tma1-server server/bin/tma1-server.exe

run: build
	./server/bin/tma1-server

dev: build
	@echo "Starting dev mode (watching server/ for changes)..."
	@trap 'kill $$PID 2>/dev/null; exit 0' INT TERM; \
	while true; do \
		./server/bin/tma1-server & PID=$$!; \
		fswatch -1 -r --exclude='/bin/' --include='\.go$$' --include='\.html$$' --include='\.css$$' --include='\.js$$' --include='\.sql$$' --exclude='.*' $(MAKEFILE_DIR)/server; \
		echo "Change detected, rebuilding..."; \
		kill $$PID 2>/dev/null; wait $$PID 2>/dev/null; \
		$(MAKE) build || continue; \
	done
