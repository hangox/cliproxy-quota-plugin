PLUGIN_NAME := cliproxy-quota-plugin

# 操作系统与动态库后缀识别
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
	LIB_EXT := dylib
	SDK_FLAG := $(shell [ -d "/Applications/Xcode.app/Contents/Developer/Platforms/MacOSX.platform/Developer/SDKs/MacOSX.sdk" ] && echo "SDKROOT=/Applications/Xcode.app/Contents/Developer/Platforms/MacOSX.platform/Developer/SDKs/MacOSX.sdk")
else ifeq ($(UNAME_S),Linux)
	LIB_EXT := so
	SDK_FLAG :=
else
	LIB_EXT := dll
	SDK_FLAG :=
endif

.PHONY: all build build-linux-amd64 test clean

all: build

# 运行纯 Go 单元测试（无需 CGO 依赖）
test:
	CGO_ENABLED=0 go test -v -count=1 ./...

# 本地平台构建 C-ABI 动态库
build:
	$(SDK_FLAG) go build -buildmode=c-shared -o $(PLUGIN_NAME).$(LIB_EXT) .

# 通过 Docker 容器为 Linux amd64 平台交叉编译 .so
build-linux-amd64:
	docker run --rm \
		-v "$$(pwd)":/src \
		-w /src \
		-e CGO_ENABLED=1 \
		-e GOOS=linux \
		-e GOARCH=amd64 \
		golang:1.26-bookworm \
		go build -buildmode=c-shared -o $(PLUGIN_NAME)-linux-amd64.so .

# 清理构建产物
clean:
	rm -f $(PLUGIN_NAME).dylib $(PLUGIN_NAME).so $(PLUGIN_NAME).dll $(PLUGIN_NAME).h $(PLUGIN_NAME)-linux-amd64.so $(PLUGIN_NAME)-linux-amd64.h
