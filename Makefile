.DEFAULT_GOAL := check

.PHONY: check test vet tidy help

check: vet test

test:
	@go test ./...

vet:
	@go vet ./...

tidy:
	@go mod tidy

# No build, no image, no bump target: there is no binary, and the version is the
# git tag. Releasing is `git tag vX.Y.Z && git push --tags` — the module proxy
# picks it up from there.
help:
	@echo "check  → vet + test (default)"
	@echo "test   → go test ./..."
	@echo "vet    → go vet ./..."
	@echo "tidy   → go mod tidy"
	@echo ""
	@echo "release: git tag vX.Y.Z && git push origin vX.Y.Z"
