.PHONY: build clean run help

BINARY_NAME=winmDNS.exe
BUILD_DIR=.
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean

help:
	@echo "Available commands:"
	@echo "  make build   - Build the application"
	@echo "  make clean   - Remove build artifacts"
	@echo "  make run     - Build and run the application"
	@echo "  make help    - Show this help message"

build:
	@echo "Building $(BINARY_NAME)..."
	@$(GOBUILD) -o $(BUILD_DIR)/$(BINARY_NAME)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

clean:
	@echo "Cleaning build artifacts..."
	@$(GOCLEAN)
	@rm -f $(BUILD_DIR)/$(BINARY_NAME)
	@echo "Clean complete"

run: build
	@echo "Running $(BINARY_NAME)..."
	@$(BUILD_DIR)/$(BINARY_NAME)

.DEFAULT_GOAL := help


