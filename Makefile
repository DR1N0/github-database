GREEN := \033[32m
BLUE := \033[34m
YELLOW := \033[33m
NC := \033[0m
.DEFAULT_GOAL := help

BINARY_NAME=github-database
BUILD_DIR=.bin
OUTPUT_DIR=.output

##@ General

.PHONY: help
help: ## Display this help message
	@echo "$(BLUE)GitHub Database$(NC)"
	@echo ""
	@echo "$(GREEN)Usage:$(NC)"
	@echo "  make <target>"
	@echo ""
	@awk 'BEGIN {FS = ":.*##"; printf "$(GREEN)Available targets:$(NC)\n"} \
		/^[a-zA-Z_0-9-]+:.*?##/ { printf "  $(BLUE)%-20s$(NC) %s\n", $$1, $$2 } \
		/^##@/ { printf "\n$(YELLOW)%s$(NC)\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: clean
clean: ## Clean build artifacts and output directories
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	@rm -rf $(OUTPUT_DIR)

.PHONY: vendor
vendor: ## Update and tidy dependencies
	@echo "Updating dependencies..."
	@go mod tidy
	@go mod vendor
	@echo "Dependencies updated."

##@ Local Development

GO_TEST_TIMEOUT ?= 30s
GO_TEST_COVERPROFILE ?= $(OUTPUT_DIR)/coverage.out

.PHONY: test
test: ## Run tests with coverage
	@mkdir -p $(OUTPUT_DIR)
	@echo "Running tests with timeout $(GO_TEST_TIMEOUT)..."
	go test -timeout $(GO_TEST_TIMEOUT) -coverprofile=$(GO_TEST_COVERPROFILE) ./...
	go tool cover -func=$(GO_TEST_COVERPROFILE)


##@ Example

.PHONY: run ## Run the example server
run:
	@cd example && go run main.go || true

.PHONY: run-offline ## Run the example server in offline mode
run-offline:
	@cd example && go run main.go --offline || true