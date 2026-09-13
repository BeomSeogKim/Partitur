# Partitur — build, install, and verification targets.
#
# Partitur ships four binaries; all four must be on PATH for a run to work:
#   partitur, partitur-adapter-codex, partitur-adapter-claude, partitur-trampoline
# `make install` puts all four where `go install` writes (`$(go env GOBIN)`, or
# `$(go env GOPATH)/bin`); that directory must be on your PATH.

BINARIES := ./cmd/partitur ./cmd/partitur-adapter-codex ./cmd/partitur-adapter-claude ./cmd/partitur-trampoline

.PHONY: install build test race faultprobe mutation battery vet fmt-check check clean help

## install: build and install all four runtime binaries onto your Go bin dir
install:
	go install $(BINARIES)

## build: compile everything without installing
build:
	go build ./...

## test: the plain test suite (CI: test job, first command)
test:
	go test ./...

## race: the race suite (CI: -race)
race:
	go test -race -count=1 -timeout=20m ./...

## faultprobe: the fault-probe catalogue suite (CI: -tags=faultprobe)
faultprobe:
	go test -tags=faultprobe -count=1 ./...

## mutation: the mutation-proof suite (CI: -tags=mutation)
mutation:
	go test -tags=mutation -count=1 -timeout=40m ./...

## battery: the full CI test battery, in CI order (plain, race, faultprobe, mutation)
battery: test race faultprobe mutation

## vet: go vet
vet:
	go vet ./...

# Scope gofmt to tracked Go files so gitignored run artifacts under .partitur/work/
# are not flagged (CI runs `gofmt -l .` on a clean checkout, where they do not exist).
## fmt-check: fail if any tracked Go file is not gofmt-clean
fmt-check:
	@unformatted=$$(git ls-files '*.go' | xargs gofmt -l); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed:"; echo "$$unformatted"; exit 1; \
	fi

## check: fmt-check, vet, build, and the full battery — the whole CI gate locally
check: fmt-check vet build battery

## clean: remove the installed binaries from the Go bin dir
clean:
	@bin=$$(go env GOBIN); [ -n "$$bin" ] || bin=$$(go env GOPATH)/bin; \
	rm -f "$$bin"/partitur "$$bin"/partitur-adapter-codex "$$bin"/partitur-adapter-claude "$$bin"/partitur-trampoline; \
	echo "removed partitur binaries from $$bin"

## help: list the targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
