.PHONY: hooks fmt vet test check up up-tunnel down logs hello

hooks:
	git config core.hooksPath .githooks
	@echo "pre-commit hook installed (core.hooksPath=.githooks)"

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')

vet:
	go vet ./...

test:
	go test ./...

check: vet test

# .env is dotenvx-encrypted; dotenvx run decrypts into the process env,
# which compose prefers over the raw .env file.
up:
	dotenvx run -- docker compose up -d --build horde

up-tunnel:
	dotenvx run -- docker compose --profile tunnel up -d --build

down:
	docker compose --profile tunnel down

logs:
	docker compose logs -f

hello:
	dotenvx run -- scripts/hello.sh
