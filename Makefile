.PHONY: test build check

build:
	go build -o bin/infra-hub ./cmd/infra-hub
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/infra-beacon ./cmd/infra-beacon
	go build -o bin/infra-deployer ./cmd/infra-deployer

test:
	node --test web/*.test.cjs
	go test -race ./...

check: test
	go vet ./...
