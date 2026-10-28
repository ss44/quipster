# syntax=docker/dockerfile:1

FROM node:22-alpine AS webbuild
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN npm install
COPY web/ ./
RUN npm run build

FROM golang:1.27-bookworm AS gobuild
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=webbuild /web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/quipster ./cmd/quipster

FROM debian:bookworm-slim

# ffmpeg (Debian builds: vaapi, qsv, nvdec; encoders vaapi/qsv only — nvenc
# needs the NVIDIA host driver mounted at runtime), plus VAAPI/DRM userland.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg ca-certificates \
    libva-drm2 libva2 libdrm2 libva-x11-2 \
    i965-va-driver intel-media-va-driver mesa-va-drivers \
    && rm -rf /var/lib/apt/lists/*

COPY --from=gobuild /out/quipster /usr/local/bin/quipster
ENV ADDR=:8080
# HWACCEL=auto probes nvenc -> qsv -> vaapi -> videotoolbox at startup and
# silently falls back to cpu when no GPU is passed through. Override with
# HWACCEL=none to force software encoding.
ENV HWACCEL=auto
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/quipster"]
