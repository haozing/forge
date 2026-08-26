ARG PGROONGA_BASE_IMAGE=groonga/pgroonga@sha256:50e95d8f0708d43f76adf641580e3b94edf57490d655316e1b9698cfbe702581

FROM ${PGROONGA_BASE_IMAGE}

ARG ALPINE_MIRROR=https://mirrors.tuna.tsinghua.edu.cn/alpine
ARG PGVECTOR_VERSION=0.8.6
ARG PGVECTOR_REPO=https://github.com/pgvector/pgvector.git

RUN sed -i "s#https://dl-cdn.alpinelinux.org/alpine#${ALPINE_MIRROR}#g" /etc/apk/repositories \
    && apk add --no-cache --virtual .pgvector-build build-base git \
    && git clone --branch "v${PGVECTOR_VERSION}" --depth 1 "${PGVECTOR_REPO}" /tmp/pgvector \
    && make -C /tmp/pgvector with_llvm=0 OPTFLAGS="" install \
    && rm -rf /tmp/pgvector \
    && apk del .pgvector-build
