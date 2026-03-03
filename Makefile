.PHONY: build run test clean fmt lint

# 变量
BINARY_NAME := gotems
BUILD_DIR := bin
MAIN_PKG := ./cmd/gotems
VERSION := 0.2.0
LDFLAGS := -ldflags "-s -w"

# 默认目标
all: fmt lint build

# 构建
build:
	@echo ">>> 构建 $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PKG)
	@echo ">>> 构建完成: $(BUILD_DIR)/$(BINARY_NAME)"

# 运行
run:
	go run $(MAIN_PKG) $(ARGS)

# 测试
test:
	go test ./... -v -race -count=1

# 格式化
fmt:
	go fmt ./...

# Lint
lint:
	@which golangci-lint > /dev/null 2>&1 || echo "提示: 安装 golangci-lint 以启用代码检查"
	@which golangci-lint > /dev/null 2>&1 && golangci-lint run ./... || true

# 清理
clean:
	rm -rf $(BUILD_DIR)

# 跨平台构建
build-all:
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 $(MAIN_PKG)
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 $(MAIN_PKG)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 $(MAIN_PKG)
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 $(MAIN_PKG)
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe $(MAIN_PKG)
	@echo ">>> 全平台构建完成"

# 示例：单 Agent 运行
example-single:
	go run $(MAIN_PKG) run "用 Go 写一个 Hello World 程序"

# 示例：竞赛模式
example-consensus:
	go run $(MAIN_PKG) run --strategy consensus "用 Go 实现快速排序"

# 示例：DAG 模式
example-dag:
	go run $(MAIN_PKG) run --dag examples/dag-blog.json

# 启动 Web 仪表盘
serve:
	go run $(MAIN_PKG) serve --addr :8080

# 启动 MCP 服务器
mcp:
	go run $(MAIN_PKG) mcp

# Docker 构建
docker-build:
	docker build -t gotems:$(VERSION) .

# Docker 运行
docker-run:
	docker run -p 8080:8080 \
		-e ANTHROPIC_API_KEY=$(ANTHROPIC_API_KEY) \
		-e GOOGLE_API_KEY=$(GOOGLE_API_KEY) \
		-e OPENAI_API_KEY=$(OPENAI_API_KEY) \
		gotems:$(VERSION)

# 示例：自动拆分模式
example-split:
	go run $(MAIN_PKG) split "实现一个完整的博客系统"

# 帮助
help:
	@go run $(MAIN_PKG) help
