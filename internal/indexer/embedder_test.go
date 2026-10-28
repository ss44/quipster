package indexer

import (
	"encoding/json"
	"strings"
	"testing"

	"quipster/internal/store"
)

// env returns a getenv func backed by a fixed map so parsing is tested
// without touching process environment.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseEmbedderConfigDisabled(t *testing.T) {
	cfg, err := ParseEmbedderConfig(env(map[string]string{
		"EMBEDDINGS_API_KEY": "ignored",
		"EMBEDDINGS_MODEL":   "also-ignored",
	}))
	if err != nil {
		t.Fatalf("ParseEmbedderConfig: %v", err)
	}
	if cfg.Enabled() {
		t.Errorf("cfg = %+v, want disabled", cfg)
	}
	// Blank source must ignore leftover variables entirely.
	if cfg.APIKey != "" || cfg.Model != "" {
		t.Errorf("disabled config leaked fields: %+v", cfg)
	}
}

func TestParseEmbedderConfigDefaults(t *testing.T) {
	cfg, err := ParseEmbedderConfig(env(map[string]string{
		"EMBEDDINGS_SOURCE":  "openai",
		"EMBEDDINGS_API_KEY": "sk-test",
	}))
	if err != nil {
		t.Fatalf("ParseEmbedderConfig: %v", err)
	}
	if cfg.Source != embedderOpenAI {
		t.Errorf("source = %q, want %q (aliases normalize)", cfg.Source, embedderOpenAI)
	}
	if cfg.DocumentTemplate != DefaultDocumentTemplate {
		t.Errorf("documentTemplate = %q, want default", cfg.DocumentTemplate)
	}
	if cfg.SemanticRatio != DefaultSemanticRatio {
		t.Errorf("semanticRatio = %v, want default", cfg.SemanticRatio)
	}
	// The canonical casing works too.
	cfg2, err := ParseEmbedderConfig(env(map[string]string{"EMBEDDINGS_SOURCE": "huggingFace"}))
	if err != nil {
		t.Fatalf("ParseEmbedderConfig: %v", err)
	}
	if cfg2.Source != embedderHuggingFace {
		t.Errorf("source = %q, want %q", cfg2.Source, embedderHuggingFace)
	}
}

func TestParseEmbedderConfigOverrides(t *testing.T) {
	cfg, err := ParseEmbedderConfig(env(map[string]string{
		"EMBEDDINGS_SOURCE":            "rest",
		"EMBEDDINGS_URL":               "http://embedder.local/embed",
		"EMBEDDINGS_DIMENSIONS":        "768",
		"EMBEDDINGS_DOCUMENT_TEMPLATE": "{{ doc.text }}",
		"EMBEDDINGS_SEMANTIC_RATIO":    "0.25",
		"EMBEDDINGS_REQUEST":           `{"text":"{{text}}"}`,
		"EMBEDDINGS_RESPONSE":          `{"embedding":"{{embedding}}"}`,
	}))
	if err != nil {
		t.Fatalf("ParseEmbedderConfig: %v", err)
	}
	if cfg.URL != "http://embedder.local/embed" || cfg.Dimensions != 768 {
		t.Errorf("url/dimensions = %q/%d", cfg.URL, cfg.Dimensions)
	}
	if cfg.DocumentTemplate != "{{ doc.text }}" {
		t.Errorf("documentTemplate = %q", cfg.DocumentTemplate)
	}
	if cfg.SemanticRatio != 0.25 {
		t.Errorf("semanticRatio = %v", cfg.SemanticRatio)
	}
	if cfg.Request["text"] != "{{text}}" || cfg.Response["embedding"] != "{{embedding}}" {
		t.Errorf("rest templates = %+v / %+v", cfg.Request, cfg.Response)
	}
}

func TestParseEmbedderConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"unknown source", map[string]string{"EMBEDDINGS_SOURCE": "ollama"}, "not supported"},
		{"openai missing key", map[string]string{"EMBEDDINGS_SOURCE": "openAi"}, "EMBEDDINGS_API_KEY"},
		{"rest missing url", map[string]string{"EMBEDDINGS_SOURCE": "rest", "EMBEDDINGS_DIMENSIONS": "8", "EMBEDDINGS_REQUEST": "{}", "EMBEDDINGS_RESPONSE": "{}"}, "EMBEDDINGS_URL"},
		{"rest missing dimensions", map[string]string{"EMBEDDINGS_SOURCE": "rest", "EMBEDDINGS_URL": "http://x", "EMBEDDINGS_REQUEST": "{}", "EMBEDDINGS_RESPONSE": "{}"}, "EMBEDDINGS_DIMENSIONS"},
		{"rest missing templates", map[string]string{"EMBEDDINGS_SOURCE": "rest", "EMBEDDINGS_URL": "http://x", "EMBEDDINGS_DIMENSIONS": "8"}, "EMBEDDINGS_REQUEST"},
		{"bad dimensions", map[string]string{"EMBEDDINGS_SOURCE": "huggingFace", "EMBEDDINGS_DIMENSIONS": "zero"}, "EMBEDDINGS_DIMENSIONS"},
		{"bad ratio", map[string]string{"EMBEDDINGS_SOURCE": "huggingFace", "EMBEDDINGS_SEMANTIC_RATIO": "2"}, "EMBEDDINGS_SEMANTIC_RATIO"},
		{"bad request json", map[string]string{"EMBEDDINGS_SOURCE": "rest", "EMBEDDINGS_URL": "http://x", "EMBEDDINGS_DIMENSIONS": "8", "EMBEDDINGS_REQUEST": "["}, "EMBEDDINGS_REQUEST"},
		{"bad response json", map[string]string{"EMBEDDINGS_SOURCE": "rest", "EMBEDDINGS_URL": "http://x", "EMBEDDINGS_DIMENSIONS": "8", "EMBEDDINGS_RESPONSE": "nope"}, "EMBEDDINGS_RESPONSE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseEmbedderConfig(env(tc.env))
			if err == nil {
				t.Fatalf("want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestEmbedderSettingsPatch(t *testing.T) {
	cfg := EmbedderConfig{
		Source: embedderOpenAI, URL: "http://ollama:11434/v1", APIKey: "placeholder",
		Model: "nomic-embed-text", Dimensions: 768,
		DocumentTemplate: DefaultDocumentTemplate, SemanticRatio: 0.9,
	}
	body, err := cfg.settingsPatch()
	if err != nil {
		t.Fatalf("settingsPatch: %v", err)
	}
	var got map[string]map[string]map[string]any
	if uerr := json.Unmarshal(body, &got); uerr != nil {
		t.Fatalf("body %q: %v", body, uerr)
	}
	emb := got["embedders"][EmbedderName]
	if emb == nil {
		t.Fatalf("body = %q, want a default embedder", body)
	}
	if emb["source"] != embedderOpenAI || emb["url"] != "http://ollama:11434/v1" || emb["apiKey"] != "placeholder" {
		t.Errorf("embedder identity fields = %+v", emb)
	}
	if emb["model"] != "nomic-embed-text" || emb["dimensions"] != float64(768) {
		t.Errorf("embedder model fields = %+v", emb)
	}
	if emb["documentTemplate"] != DefaultDocumentTemplate {
		t.Errorf("documentTemplate = %v", emb["documentTemplate"])
	}
	if _, ok := emb["semanticRatio"]; ok {
		t.Errorf("semanticRatio must not leak into the embedder settings: %+v", emb)
	}

	// huggingFace minimal config renders only the source and template.
	hf := EmbedderConfig{Source: embedderHuggingFace, DocumentTemplate: DefaultDocumentTemplate}
	body, err = hf.settingsPatch()
	if err != nil {
		t.Fatalf("settingsPatch: %v", err)
	}
	if string(body) != `{"embedders":{"default":{"documentTemplate":"{{doc.text}}","source":"huggingFace"}}}` {
		t.Errorf("huggingFace body = %q", body)
	}
}

func TestEnsureSettingsAppliesEmbedders(t *testing.T) {
	f := newFakeMeili(t)
	st := newTestStore(t)
	cfg, err := ParseEmbedderConfig(env(map[string]string{
		"EMBEDDINGS_SOURCE": "openai", "EMBEDDINGS_API_KEY": "sk", "EMBEDDINGS_MODEL": "m",
	}))
	if err != nil {
		t.Fatal(err)
	}
	ix := New(st, f.url(), "", WithEmbedder(cfg))
	if err := ix.EnsureSettings(); err != nil {
		t.Fatalf("EnsureSettings: %v", err)
	}
	if f.embedderCalls != 1 {
		t.Fatalf("embedder patch calls = %d, want 1", f.embedderCalls)
	}
	bodies := f.patchBodies()
	// The client itself patches base settings, then typo tolerance, then the
	// embedder configuration.
	if len(bodies) != 3 {
		t.Fatalf("patch bodies = %d, want settings + typo + embedder", len(bodies))
	}
	if !strings.Contains(bodies[2], `"embedders"`) || !strings.Contains(bodies[2], `"source":"openAi"`) {
		t.Errorf("embedder patch body = %q", bodies[2])
	}

	// A plain indexer must leave embedder settings untouched.
	f2 := newFakeMeili(t)
	ix2 := New(newTestStore(t), f2.url(), "")
	if err := ix2.EnsureSettings(); err != nil {
		t.Fatalf("EnsureSettings disabled: %v", err)
	}
	if f2.embedderCalls != 0 {
		t.Errorf("disabled indexer patched embedders %d times", f2.embedderCalls)
	}
}

func TestEnsureSettingsEmbedderFailures(t *testing.T) {
	t.Run("patch rejected", func(t *testing.T) {
		f := newFakeMeili(t)
		f.failEmbedder = true
		ix := New(newTestStore(t), f.url(), "", WithEmbedder(EmbedderConfig{Source: embedderHuggingFace, DocumentTemplate: DefaultDocumentTemplate}))
		if err := ix.EnsureSettings(); err == nil || !strings.Contains(err.Error(), "embedder settings") {
			t.Errorf("err = %v, want embedder settings failure", err)
		}
	})
	t.Run("task failed", func(t *testing.T) {
		f := newFakeMeili(t)
		f.failEmbTask = true
		ix := New(newTestStore(t), f.url(), "", WithEmbedder(EmbedderConfig{Source: embedderHuggingFace, DocumentTemplate: DefaultDocumentTemplate}))
		if err := ix.EnsureSettings(); err == nil || !strings.Contains(err.Error(), "task failed") {
			t.Errorf("err = %v, want failed embedder task", err)
		}
	})
}

func TestIndexerEmbedderAccessors(t *testing.T) {
	st := &store.Store{}
	plain := New(st, "http://localhost:7700", "")
	if plain.EmbedderEnabled() || plain.EmbedderName() != "" {
		t.Errorf("plain indexer reports semantic search: %v/%q", plain.EmbedderEnabled(), plain.EmbedderName())
	}
	if plain.SemanticRatio() != 0 {
		t.Errorf("plain ratio = %v, want 0", plain.SemanticRatio())
	}

	ratioCfg := EmbedderConfig{Source: embedderREST, URL: "http://x", SemanticRatio: 9}
	enabled := New(st, "http://localhost:7700", "", WithEmbedder(ratioCfg))
	if !enabled.EmbedderEnabled() || enabled.EmbedderName() != EmbedderName {
		t.Errorf("enabled indexer = %v/%q", enabled.EmbedderEnabled(), enabled.EmbedderName())
	}
	// Out-of-range ratios clamp to the range Meilisearch accepts.
	if got := enabled.SemanticRatio(); got != 1 {
		t.Errorf("ratio clamp high = %v, want 1", got)
	}
	ratioCfg.SemanticRatio = -3
	low := New(st, "http://localhost:7700", "", WithEmbedder(ratioCfg))
	if got := low.SemanticRatio(); got != 0 {
		t.Errorf("ratio clamp low = %v, want 0", got)
	}
}
