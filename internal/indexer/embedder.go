package indexer

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Embedder sources supported by Meilisearch 1.11 that this deployment can
// drive without generating vectors inside Quipster itself. userProvided is
// deliberately excluded: Quipster never stores vectors in its documents, so a
// userProvided embedder would silently index nothing.
const (
	embedderOpenAI      = "openAi"
	embedderHuggingFace = "huggingFace"
	embedderREST        = "rest"
)

// EmbedderName is the Meilisearch embedder key Quipster installs and queries.
const EmbedderName = "default"

// DefaultDocumentTemplate renders only the spoken text into the embedding
// input, keeping vectors focused on subtitle content instead of leaking ids,
// titles or timestamps into the vector space.
const DefaultDocumentTemplate = "{{doc.text}}"

// DefaultSemanticRatio is the keyword/semantic blend used for hybrid queries
// when the operator does not override EMBEDDINGS_SEMANTIC_RATIO. 0.5 matches
// the Meilisearch default.
const DefaultSemanticRatio = 0.5

// EmbedderConfig describes the semantic-search embedder installed on the
// Meilisearch index. Meilisearch generates the document embeddings itself
// during the ingestion phase (each AddDocuments batch) using this
// configuration and the document template, so Quipster never computes or
// stores vectors locally.
//
// SemanticRatio is not part of the embedder settings payload; it is the
// keyword/semantic blend Quipster applies when issuing hybrid queries.
type EmbedderConfig struct {
	Request          map[string]any
	Response         map[string]any
	Source           string
	URL              string
	APIKey           string
	Model            string
	DocumentTemplate string
	Dimensions       int
	SemanticRatio    float64
}

// Enabled reports whether semantic search is configured.
func (c EmbedderConfig) Enabled() bool { return c.Source != "" }

// normalizeSource canonicalizes the operator-supplied source name. Unknown
// values are returned unchanged so validation can report them verbatim.
func normalizeSource(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "openai":
		return embedderOpenAI
	case "huggingface":
		return embedderHuggingFace
	case "rest":
		return embedderREST
	case "":
		return ""
	default:
		return strings.TrimSpace(v)
	}
}

// ParseEmbedderConfig reads the EMBEDDINGS_* environment variables through
// getenv and validates the result. An empty EMBEDDINGS_SOURCE disables
// semantic search and yields a zero config, ignoring any leftover variables.
func ParseEmbedderConfig(getenv func(string) string) (EmbedderConfig, error) {
	cfg := EmbedderConfig{
		Source:           normalizeSource(getenv("EMBEDDINGS_SOURCE")),
		URL:              strings.TrimSpace(getenv("EMBEDDINGS_URL")),
		APIKey:           strings.TrimSpace(getenv("EMBEDDINGS_API_KEY")),
		Model:            strings.TrimSpace(getenv("EMBEDDINGS_MODEL")),
		DocumentTemplate: strings.TrimSpace(getenv("EMBEDDINGS_DOCUMENT_TEMPLATE")),
	}
	if !cfg.Enabled() {
		return EmbedderConfig{}, nil
	}
	if cfg.DocumentTemplate == "" {
		cfg.DocumentTemplate = DefaultDocumentTemplate
	}
	if v := strings.TrimSpace(getenv("EMBEDDINGS_DIMENSIONS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return EmbedderConfig{}, fmt.Errorf("EMBEDDINGS_DIMENSIONS %q must be a positive integer", v)
		}
		cfg.Dimensions = n
	}
	cfg.SemanticRatio = DefaultSemanticRatio
	if v := strings.TrimSpace(getenv("EMBEDDINGS_SEMANTIC_RATIO")); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 1 {
			return EmbedderConfig{}, fmt.Errorf("EMBEDDINGS_SEMANTIC_RATIO %q must be a number between 0 and 1", v)
		}
		cfg.SemanticRatio = f
	}
	// The REST embedder templates are arbitrary JSON request/response shapes
	// that differ per provider, so operators pass them as raw JSON strings.
	for _, kv := range []struct {
		dst *map[string]any
		key string
	}{{&cfg.Request, "EMBEDDINGS_REQUEST"}, {&cfg.Response, "EMBEDDINGS_RESPONSE"}} {
		if v := strings.TrimSpace(getenv(kv.key)); v != "" {
			m := map[string]any{}
			if err := json.Unmarshal([]byte(v), &m); err != nil {
				return EmbedderConfig{}, fmt.Errorf("%s must be a JSON object: %w", kv.key, err)
			}
			*kv.dst = m
		}
	}
	if err := cfg.Validate(); err != nil {
		return EmbedderConfig{}, err
	}
	return cfg, nil
}

// Validate checks the embedder configuration against the requirements of the
// selected source so misconfiguration surfaces at startup instead of as a
// failed Meilisearch settings task.
func (c EmbedderConfig) Validate() error {
	if !c.Enabled() {
		return nil
	}
	switch c.Source {
	case embedderOpenAI:
		// Meilisearch requires an API key for the openAi source even when url
		// points at a local OpenAI-compatible server (Ollama, LM Studio,
		// vLLM); such deployments can pass any placeholder value.
		if c.APIKey == "" {
			return errors.New("openAi embedder requires EMBEDDINGS_API_KEY (local OpenAI-compatible servers accept a placeholder value)")
		}
	case embedderHuggingFace:
		// Nothing is required: Meilisearch downloads its default model on
		// first use and embeds locally from then on.
	case embedderREST:
		if c.URL == "" {
			return errors.New("rest embedder requires EMBEDDINGS_URL")
		}
		if c.Dimensions <= 0 {
			return errors.New("rest embedder requires EMBEDDINGS_DIMENSIONS")
		}
		if c.Request == nil || c.Response == nil {
			return errors.New("rest embedder requires EMBEDDINGS_REQUEST and EMBEDDINGS_RESPONSE JSON templates")
		}
	default:
		return fmt.Errorf("EMBEDDINGS_SOURCE %q is not supported; use openAi, huggingFace or rest", c.Source)
	}
	return nil
}

// settingsPatch renders the partial settings body that installs this embedder
// under the "default" key. It is applied with a raw PATCH because the Go
// client serializes embedder fields Meilisearch 1.11 rejects (the same
// constraint applyTypoTolerance works around).
func (c EmbedderConfig) settingsPatch() ([]byte, error) {
	emb := map[string]any{"source": c.Source}
	if c.URL != "" {
		emb["url"] = c.URL
	}
	if c.APIKey != "" {
		emb["apiKey"] = c.APIKey
	}
	if c.Model != "" {
		emb["model"] = c.Model
	}
	if c.Dimensions > 0 {
		emb["dimensions"] = c.Dimensions
	}
	if c.DocumentTemplate != "" {
		emb["documentTemplate"] = c.DocumentTemplate
	}
	if c.Request != nil {
		emb["request"] = c.Request
	}
	if c.Response != nil {
		emb["response"] = c.Response
	}
	body := map[string]any{"embedders": map[string]any{EmbedderName: emb}}
	return json.Marshal(body)
}
