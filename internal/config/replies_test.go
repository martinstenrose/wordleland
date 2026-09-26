package config

import (
	"strings"
	"testing"
)

// Replies are on with the rest of the bridge, and the model has the
// compose file's address and a default model, so nothing about it has to
// be configured by hand.
func TestLoadBridgeRepliesDefaultOn(t *testing.T) {
	setEnv(t, bridgeEnv())

	cfg, err := LoadBridge()
	if err != nil {
		t.Fatalf("LoadBridge() failed: %v", err)
	}
	if !cfg.Replies {
		t.Error("Replies = false, want true by default")
	}
	if cfg.LLMURL != DefaultLLMURL {
		t.Errorf("LLMURL = %q, want the default %q", cfg.LLMURL, DefaultLLMURL)
	}
	if cfg.LLMModel != DefaultLLMModel {
		t.Errorf("LLMModel = %q, want the default %q", cfg.LLMModel, DefaultLLMModel)
	}
}

func TestLoadBridgeRepliesCanBeDisabledAndTheModelChosen(t *testing.T) {
	env := bridgeEnv()
	env["SIGNAL_REPLIES"] = "false"
	env["LLM_MODEL"] = "llama3.2:3b"
	setEnv(t, env)

	cfg, err := LoadBridge()
	if err != nil {
		t.Fatalf("LoadBridge() failed: %v", err)
	}
	if cfg.Replies {
		t.Error("Replies = true, want false with SIGNAL_REPLIES=false")
	}
	if cfg.LLMModel != "llama3.2:3b" {
		t.Errorf("LLMModel = %q, want the override", cfg.LLMModel)
	}
}

// A bad model URL is refused only when replies would use it.
func TestLoadBridgeChecksTheModelURLOnlyWhenRepliesAreOn(t *testing.T) {
	env := bridgeEnv()
	env["LLM_URL"] = "ollama:11434"
	setEnv(t, env)
	if _, err := LoadBridge(); err == nil || !strings.Contains(err.Error(), "LLM_URL") {
		t.Errorf("a schemeless LLM_URL loaded with replies on: %v", err)
	}

	env["SIGNAL_REPLIES"] = "false"
	setEnv(t, env)
	if _, err := LoadBridge(); err != nil {
		t.Errorf("a bad LLM_URL was refused with replies off: %v", err)
	}
}
