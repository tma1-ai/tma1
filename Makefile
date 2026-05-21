MAKEFILE_DIR:=$(shell dirname $(realpath $(firstword $(MAKEFILE_LIST))))
GO_PACKAGES:=./cmd/... ./internal/... ./web

.PHONY: build build-linux build-windows vet lint lint-js test check install install-hooks clean run dev sync-plugin sync-skills

# Where `make install` lays down the binary. Matches the install.sh convention
# so the dev build slots into the same path the released installer uses
# (override via TMA1_INSTALL_DIR or `make install INSTALL_DIR=/somewhere`).
INSTALL_DIR ?= $(or $(TMA1_INSTALL_DIR),$(HOME)/.tma1/bin)

build: sync-plugin
	mkdir -p $(MAKEFILE_DIR)/server/bin
	cd server && CGO_ENABLED=0 go build -o ./bin/tma1-server ./cmd/tma1-server

# sync-plugin mirrors claude-plugin/{skills,commands}/ (canonical source — end
# users get this via the published plugin) into server/internal/hooks/{skills,
# commands}/ where the trees are baked into the binary via go:embed. Always
# runs before build so the embedded copies can't drift from the plugin source.
sync-plugin:
	@rm -rf $(MAKEFILE_DIR)/server/internal/hooks/skills
	@cp -R $(MAKEFILE_DIR)/claude-plugin/skills $(MAKEFILE_DIR)/server/internal/hooks/skills
	@rm -rf $(MAKEFILE_DIR)/server/internal/hooks/commands
	@cp -R $(MAKEFILE_DIR)/claude-plugin/commands $(MAKEFILE_DIR)/server/internal/hooks/commands
	@echo "synced claude-plugin/{skills,commands}/ -> server/internal/hooks/"

# Back-compat alias: pre-existing scripts / hooks may still target sync-skills.
sync-skills: sync-plugin

build-linux:
	mkdir -p $(MAKEFILE_DIR)/server/bin
	cd server && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ./bin/tma1-server ./cmd/tma1-server

build-windows:
	mkdir -p $(MAKEFILE_DIR)/server/bin
	cd server && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o ./bin/tma1-server.exe ./cmd/tma1-server

vet:
	cd server && go vet $(GO_PACKAGES)

lint:
	cd server && golangci-lint run $(GO_PACKAGES)

lint-js:
	cd server/web && npx eslint js/

test:
	cd server && go test -race -count=1 $(GO_PACKAGES)

# Run all checks CI runs (vet, lint, test, lint-js). Used by the pre-push hook.
check: vet lint test lint-js

# Install the dev build to $(INSTALL_DIR) — same path the released installer
# uses. Does NOT restart the running tma1-server; that's a deliberate choice
# because killing it interrupts any active CC sessions and forces GreptimeDB
# to restart. Prints the exact restart command at the end.
install: build
	@mkdir -p $(INSTALL_DIR)
	@install -m 0755 $(MAKEFILE_DIR)/server/bin/tma1-server $(INSTALL_DIR)/tma1-server
	@echo "installed: $(INSTALL_DIR)/tma1-server"
	@if pgrep -f "$(INSTALL_DIR)/tma1-server" >/dev/null 2>&1 || pgrep -f "server/bin/tma1-server" >/dev/null 2>&1; then \
		echo ""; \
		echo "⚠  tma1-server is running — restart it to pick up the new build:"; \
		echo "    pkill -f tma1-server   # SIGTERM, graceful shutdown"; \
		echo "    $(INSTALL_DIR)/tma1-server &"; \
	fi

# Install the repo's git hooks so pre-push runs the same checks CI runs.
install-hooks:
	git config core.hooksPath .githooks
	chmod +x .githooks/pre-push 2>/dev/null || true
	@echo "git hooks installed (core.hooksPath=.githooks)"
	@echo "bypass once with: GIT_PUSH_SKIP_HOOKS=1 git push"

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
