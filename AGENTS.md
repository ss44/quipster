# Quipster AI Agent Guidelines

This document outlines the standard operating procedures, architectural guidelines, and coding standards for all AI agents and developers contributing to the Quipster repository.

## 1. Commit Message Conventions

We strictly enforce a uniform commit message format to maintain a readable and organized project history. Follow this convention when creating new PR's and setting PR titles also!!

**Format:** `<DOMAIN>: <summary of changes>`
*(Note: Start the summary with a lowercase letter and use the imperative mood. Do not end the summary with a period.)*

**Valid Domains:**
Based on the repository's history and architecture, you must use one of the following exact domain tags:
- `BACKEND`: For changes to the Go backend services, API router, data pipelines, and SQLite store.
- `ADMIN`: For changes related to administration features, queue control, multi-folder library settings, and ops endpoints.
- `UI`: For frontend Vue3/Vuetify3 components, design system overhauls, and client-side logic.
- `CLIPS`: For updates to FFmpeg processing, thumbnail extraction, keyframe snapping, video/audio mapping, and caching.
- `SEARCH`: For changes affecting MeiliSearch indexing, document ingestion, and search APIs.
- `INFRA`: For infrastructure adjustments, Dockerfiles, Docker Compose configurations, and GitHub Actions (CI).
- `DOCS`: For changes strictly relating to READMEs, markdown documentation, or licensing.

*Example:* `BACKEND: fix subtitle extraction concurrency limits on NFS`

## 2. Architecture and Design Standards

- **SOLID Principles:** Enforce a clean, modular structure. Ensure that functions have a single responsibility, interfaces are strictly segregated, and dependencies are cleanly injected.
- **Maintainability & Readability:** Code must prioritize human readability over cleverness. Avoid deeply nested structures and excessive complexity. 
- **Decoupled Architecture:** Maintain strict boundaries between HTTP routing layers (e.g., `chi`), business logic, data persistence (`SQLite`), and external services (`MeiliSearch`, `FFmpeg`).

## 3. Go Documentation & Commenting Standards

- **Idiomatic Go Docs:** Adhere to [Effective Go](https://go.dev/doc/effective_go) documentation practices. Every exported identifier (packages, structs, interfaces, functions, variables) **must** have a preceding documentation comment starting with the name of the identifier.
- **Intent Over Mechanics:** Inside functions, comments should explain the *why* and the *intent* of the implementation rather than restating the code structure. 
- **Non-Trivial Logic:** Ensure core business logic (e.g., filename parsing heuristics, FFmpeg stream mapping, search index latching) is extensively documented so that the context and edge cases are preserved for future developers.

## 4. Linting and Testing (Pre-Commit Requirements)

Before finalizing any changes or attempting to commit code, AI agents **must** ensure the codebase passes all tests, meets our strict linting standards, and maintains at least 85% test coverage.

1. **Format and Lint:** Run `golangci-lint run`. The configuration in `.golangci.yml` enforces standards for formatting, complexity (cyclomatic <= 15), security (`gosec`), best practices (`staticcheck`), and missing documentation. Agents must fix any reported errors.
2. **Test & Coverage:** Run `go test -coverprofile=coverage.out ./...` followed by `go tool cover -func=coverage.out | grep total`. Ensure that no tests are broken and that the total project statement coverage is **>= 85.0%**. If coverage drops below this threshold, you must write new unit tests.
3. **Pre-commit:** This repository utilizes a pre-commit hook (`.githooks/pre-commit`) that will automatically reject commits if tests fail, linters fail, or total coverage falls below 85%.

### Example Go Documentation:
```go
// SubtitleExtractor represents a service for parsing and extracting
// subtitle tracks from a given media file.
type SubtitleExtractor struct {
    // fields...
}

// Extract processes the media file at the given path and extracts the english
// audio track or subtitle stream based on the provided configuration.
// It uses a bounded concurrency model to prevent I/O hangs on NFS mounts.
func (s *SubtitleExtractor) Extract(ctx context.Context, mediaPath string) error {
    // We avoid full-file re-reads by checking the cached state first.
    // This dramatically speeds up re-probing on unchanged files.
    if s.isCached(mediaPath) {
        return nil
    }
    // ...
}
```