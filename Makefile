.PHONY: run dev build tidy test clean

#We have to lad the .evn file that
#way because otherwise the " get treated
#litteraly which would be a problem with
#the $TNS_NAMES shell variable and Oracle
# TODO: replace .env with yq-based extraction from values.dev.yaml so there is
# one source of truth for both local dev and OpenShift deployment. Also remove .env.
# https://trello.com/c/ef0qJ9TX
ENV_FILE := resources-local-development-only/.env

define load_env
	set -a; \
	[ -f $(ENV_FILE) ] && . ./$(ENV_FILE); \
	set +a;
endef

# := evaluates the shell command once when make reads the Makefile, not on every recipe
# execution. This means "git rev-parse" runs once per "make" invocation regardless of
# how many targets are built, which is both fast and consistent within a single build.
GIT_COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
# "git diff --quiet" exits 1 when the working tree is dirty; the || clause appends "-dirty"
# so the footer shows e.g. "dev (a3f2c1d-dirty)" when there are uncommitted changes.
GIT_DIRTY   := $(shell git diff --quiet 2>/dev/null || echo -dirty)
# -X patches the named package-level variable directly into the binary at link time.
# No commit is required — dirty state is captured by the -dirty suffix above.
GO_LDFLAGS  := -X 'github.com/TheRealHaui/Safety-Automation/internal/version.Commit=$(GIT_COMMIT)$(GIT_DIRTY)'

#That does not work since removing the web/templates directory under server too
#run:
    #go run ./cmd/server/...
    #That works
    #No not anmyore now too
    #Problem with Oracle connection
    #But source .env && run ./cmd/server/main.go direclty from the command line works

# GO_LDFLAGS injects the git commit into the binary so the web UI footer shows
# the exact source state (e.g. "dev (a3f2c1d-dirty)") for every local run.
run:
	@$(load_env) \
		go run -ldflags="$(GO_LDFLAGS)" ./cmd/server/main.go

# Air handles ldflags via its own cmd in .air.toml — no need to pass them here.
dev:
	@$(load_env) \
		air

# GO_LDFLAGS bakes the commit hash into the released binary, matching what
# "make run" shows during development so version strings are always traceable.
build:
	go build -ldflags="$(GO_LDFLAGS)" -o bin/resilience-tester ./cmd/server/...

tidy:
	go mod tidy

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf bin/
