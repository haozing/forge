ARG GO_BASE_IMAGE=golang:1.27.0-bookworm
ARG GO_PROXY=https://goproxy.cn,direct

FROM ${GO_BASE_IMAGE} AS build

ARG GO_PROXY

WORKDIR /src

COPY go.mod go.sum ./
RUN GOPROXY=${GO_PROXY} go mod download

COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/agentchunzhi-api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/agentchunzhi-worker ./cmd/worker
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/agentchunzhi-migrate ./cmd/migrate
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/agentchunzhi-bootstrap ./cmd/bootstrap

FROM ${GO_BASE_IMAGE} AS runtime

# D 截图池（方案 §10.2）：chromium 供 RENDERER_ENABLED=true 时设计会话
# observe 截图；fonts-noto-cjk 保证中文内容截图不出现豆腐块。
# chromium 以 --no-sandbox 运行（见 internal/screenshot），需 HOME 可写。
RUN apt-get update     && apt-get install -y --no-install-recommends chromium fonts-noto-cjk     && rm -rf /var/lib/apt/lists/*

ENV HOME=/tmp

WORKDIR /app

COPY --from=build /out/agentchunzhi-api /app/agentchunzhi-api
COPY --from=build /out/agentchunzhi-worker /app/agentchunzhi-worker
COPY --from=build /out/agentchunzhi-migrate /app/agentchunzhi-migrate
COPY --from=build /out/agentchunzhi-bootstrap /app/agentchunzhi-bootstrap
COPY db/migrations /app/db/migrations

USER 65532:65532

CMD ["/app/agentchunzhi-api"]
