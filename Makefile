.PHONY: run test test-race


test:
	@go test ./...


test-race:
	@go test ./... -race


run:
	@go run .
