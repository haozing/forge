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

FROM ${GO_BASE_IMAGE} AS runtime

WORKDIR /app

COPY --from=build /out/agentchunzhi-api /app/agentchunzhi-api
COPY --from=build /out/agentchunzhi-worker /app/agentchunzhi-worker
COPY db/migrations /app/db/migrations

USER 65532:65532

CMD ["/app/agentchunzhi-api"]
