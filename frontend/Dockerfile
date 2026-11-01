# Dependency Time Machine — operator workbench (frontend/)
#
# Build context: product root.
#   docker build -f frontend/Dockerfile -t dtm-frontend .
#
# Multi-stage: pnpm build (Node 20, matching README's "Node 20+") →
# nginx serving the static dist/ with /api and /v1 reverse-proxied to the
# dtm-server container. The dev setup in vite.config.ts proxies /v1 to the
# control plane; in the containerised shape nginx does the same job so the
# frontend stays same-origin with its API (see frontend/src/api/client.ts:
# BASE = "" / same-origin fetch).

# ---- builder ---------------------------------------------------------------
FROM node:20-alpine AS build
RUN apk add --no-cache curl
RUN corepack enable pnpm
WORKDIR /app

# pnpm 9 is the era-correct major for Vite 5 / React 18 tooling here; the
# repo has no packageManager field or lockfile yet (see deploy/NOTES-AND-GAPS.md),
# so pin explicitly.
ARG PNPM_VERSION=9.15.0
RUN corepack prepare pnpm@${PNPM_VERSION} --activate

COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

COPY frontend/ ./
RUN pnpm build

# ---- runtime ---------------------------------------------------------------
FROM nginx:1.27-alpine

# nginx reverse-proxy config lives in deploy/ (owned by the deployment head);
# build context is the product root.
COPY deploy/dtm-frontend-nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html

EXPOSE 80

# Self-contained healthcheck: serving the SPA index (does NOT depend on
# dtm-server being up; the /v1, /api, /healthz proxy locations exist so
# the workbench reaches the control plane same-origin).
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD wget -qO /dev/null http://127.0.0.1:80/ || exit 1
