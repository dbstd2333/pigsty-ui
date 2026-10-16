# Pigsty Console 后端构建入口（前端源码在 console/web/）
GO   ?= go
PNPM ?= pnpm
TAGS ?= embedui

.PHONY: build build-api dev-api schema test lint clean

# build 生成内嵌前端的单二进制
build:
	$(PNPM) -C web build
	$(GO) build -tags $(TAGS) -o pigsty-console ./cmd/pigsty-console

# build-api 只编译后端，非 /api 路径返回开发提示页
build-api:
	$(GO) build -o pigsty-console ./cmd/pigsty-console

# dev-api 开发模式启动（配合 pnpm -C web dev 的 Vite 代理）
dev-api:
	$(GO) run ./cmd/pigsty-console serve --pigsty-home .. --data-dir ./.data

# schema 重新生成参数 Schema（产物需提交）
schema:
	$(GO) run ./tools/schemagen --pigsty-home .. --out schema/v4.5.0.json

test:
	$(GO) test ./...

lint:
	$(GO) vet ./...

clean:
	rm -f pigsty-console
