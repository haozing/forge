# Docker 国内镜像与构建说明

本项目的 Compose 构建默认使用国内镜像入口，适合中国大陆网络环境。所有镜像和软件源都可以通过环境变量替换为企业 Harbor、离线缓存或其他镜像站。

## 已接入的镜像

| 用途 | 默认地址 | 说明 |
| --- | --- | --- |
| Go 构建和运行时 | `docker.m.daocloud.io/library/golang:1.22-bookworm` | DaoCloud Docker Hub 代理，API 和 Worker 已完成构建验证 |
| PGroonga 基础镜像 | `docker.m.daocloud.io/groonga/pgroonga@sha256:50e95d8f0708d43f76adf641580e3b94edf57490d655316e1b9698cfbe702581` | 保留 digest 固定版本，已验证可以拉取 |
| Alpine 软件源 | `https://mirrors.tuna.tsinghua.edu.cn/alpine` | PGVector 编译依赖的 `apk` 源 |

`docker.m.daocloud.io/pgvector/pgvector:pg18` 也可以拉取，但它只包含 PGVector，不包含 PGroonga，不能直接替代本项目的 PostgreSQL 基础镜像。项目仍然在 PGroonga 镜像上编译并安装 PGVector，以同时满足全文检索和向量检索需求。

## 配置入口

复制 `.env.example` 为 `.env` 后按部署环境修改：

```dotenv
GO_BASE_IMAGE=docker.m.daocloud.io/library/golang:1.27.0-bookworm
GO_PROXY=https://goproxy.cn,direct
PGROONGA_BASE_IMAGE=docker.m.daocloud.io/groonga/pgroonga@sha256:50e95d8f0708d43f76adf641580e3b94edf57490d655316e1b9698cfbe702581
ALPINE_MIRROR=https://mirrors.tuna.tsinghua.edu.cn/alpine
PGVECTOR_VERSION=0.8.6
PGVECTOR_REPO=https://github.com/pgvector/pgvector.git
```

企业内网可以将前三项指向内部镜像，将 `PGVECTOR_REPO` 指向经过审计的源码缓存。不要把带凭据的私有仓库地址提交到仓库。

Compose 在未提供 `.env` 时会使用开发 OSS 默认值，仅用于启动和本地联调；生产环境必须通过 `.env` 或部署平台注入真实 OSS bucket 和凭据。

## 常用验证命令

```bash
docker manifest inspect docker.m.daocloud.io/library/golang:1.22-bookworm
docker pull docker.m.daocloud.io/groonga/pgroonga:latest
docker compose config --quiet
docker compose build api worker
```

PostgreSQL 自定义镜像首次构建还需要编译 PGVector，耗时明显高于 API/Worker 构建；构建完成后会由 Compose 本地缓存，后续只在 Dockerfile、版本或构建参数变化时重新执行。PGroonga 的 Alpine 基础镜像没有 `clang-21`，Dockerfile 使用 `with_llvm=0` 关闭 PGXS bitcode 生成，但不影响 PGVector 的正常运行。

已完成运行时验证：临时 PostgreSQL 容器中可以同时执行 `CREATE EXTENSION pgroonga`（4.0.8）和 `CREATE EXTENSION vector`（0.8.6）。

已完成隔离 Compose smoke test：全新 PostgreSQL 数据卷完成迁移，API 和 Worker 同时启动稳定，`GET /readyz` 返回 HTTP 200。现有开发数据卷未重置；如果其中记录了旧迁移 checksum，应用会按设计拒绝继续迁移，需要在确认数据处理策略后再单独处理。
