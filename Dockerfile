# One image for every EventMesh process (api, worker, receiver, migrate, seed).
# The compose file picks the command. Three stages keep compilers and node_modules
# out of the final image.

# ---- 1. frontend: static files only ----
FROM node:lts-alpine AS frontend
WORKDIR /src/frontend
# Dependencies first: this layer is reused until package-lock.json changes.
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# ---- 2. backend: static Go binaries ----
FROM golang:1.26-alpine AS backend
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# CGO off gives static binaries that run on a bare base image.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/api ./cmd/worker ./cmd/receiver ./cmd/migrate ./cmd/seed

# ---- 3. runtime ----
FROM alpine:3
# ca-certificates: the worker makes HTTPS requests to customer endpoints.
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 eventmesh
COPY --from=backend /out/ /app/bin/
COPY --from=frontend /src/frontend/dist /app/frontend
ENV PATH="/app/bin:${PATH}" \
    EM_FRONTEND_DIR=/app/frontend
WORKDIR /app
# Never run as root: a bug in the app should not be a root shell in the container.
USER eventmesh
EXPOSE 8090 9000
CMD ["api"]
