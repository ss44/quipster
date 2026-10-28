#!/usr/bin/env bash
set -e

echo "🚀 Starting Quipster Dev Environment..."

# Ensure required commands exist
if ! command -v docker &> /dev/null; then
    echo "❌ Docker is required but not installed."
    exit 1
fi

# 1. Start Meilisearch if nothing is listening on 7700
if ! docker ps | grep -q "7700->7700"; then
    echo "📦 Starting ephemeral Meilisearch container..."
    docker run -d --rm --name quipster-meilisearch-dev -p 7700:7700 -e MEILI_MASTER_KEY=dev-key -e MEILI_NO_ANALYTICS=true getmeili/meilisearch:v1.11 > /dev/null
    STARTED_MEILI=true
else
    echo "✅ Meilisearch is already running."
fi

# 2. Generate fixtures if needed
if [ ! -d "testdata/Movies" ]; then
    echo "🎬 Generating test fixtures..."
    sh testdata/make_fixtures.sh
fi

# 3. Setup required directories
mkdir -p cache data

# 4. Clean up background processes on exit
cleanup() {
    echo -e "\n🛑 Stopping services..."
    kill $(jobs -p) 2>/dev/null || true
    if [ "$STARTED_MEILI" = true ]; then
        echo "🧹 Stopping Meilisearch container..."
        docker stop quipster-meilisearch-dev > /dev/null || true
    fi
}
trap cleanup EXIT

# 5. Start Go backend in the background
echo "🐹 Starting Go API..."
MEDIA_DIR=./testdata CACHE_DIR=./cache DATA_DIR=./data MEILI_MASTER_KEY=dev-key go run ./cmd/quipster &

# 6. Start Frontend in the foreground
echo "⚛️ Starting Vue frontend..."
cd web
if [ ! -d "node_modules" ]; then
    echo "📦 Installing frontend dependencies..."
    npm install
fi
npm run dev
