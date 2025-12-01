package droid

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

// GetDroidModels returns the model definitions available through Factory Droid.
func GetDroidModels() []*registry.ModelInfo {
	return []*registry.ModelInfo{
		{
			ID:                  "droid-glm-4.6",
			Object:              "model",
			Created:             1759190400,
			OwnedBy:             "factory",
			Type:                "droid",
			DisplayName:         "Droid Core (GLM-4.6)",
			Description:         "Fast and cost-effective model via Factory Droid",
			ContextLength:       128000,
			MaxCompletionTokens: 32000,
		},
		{
			ID:                  "droid-claude-haiku-4.5",
			Object:              "model",
			Created:             1759276800,
			OwnedBy:             "factory",
			Type:                "droid",
			DisplayName:         "Claude Haiku 4.5 (via Droid)",
			Description:         "Fast Claude model via Factory Droid",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "droid-claude-sonnet-4.5",
			Object:              "model",
			Created:             1759104000,
			OwnedBy:             "factory",
			Type:                "droid",
			DisplayName:         "Claude Sonnet 4.5 (via Droid)",
			Description:         "Balanced Claude model via Factory Droid",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "droid-claude-opus-4.5",
			Object:              "model",
			Created:             1761955200,
			OwnedBy:             "factory",
			Type:                "droid",
			DisplayName:         "Claude Opus 4.5 (via Droid)",
			Description:         "Most capable Claude model via Factory Droid",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:                  "droid-gpt-5.1-codex",
			Object:              "model",
			Created:             1762905600,
			OwnedBy:             "factory",
			Type:                "droid",
			DisplayName:         "GPT-5.1 Codex (via Droid)",
			Description:         "OpenAI coding model via Factory Droid",
			ContextLength:       400000,
			MaxCompletionTokens: 128000,
		},
		{
			ID:                  "droid-gemini-3-pro",
			Object:              "model",
			Created:             1737158400,
			OwnedBy:             "factory",
			Type:                "droid",
			DisplayName:         "Gemini 3 Pro (via Droid)",
			Description:         "Google Gemini model via Factory Droid",
			ContextLength:       1048576,
			MaxCompletionTokens: 65536,
		},
	}
}

// MapToNativeModel converts CLIProxyAPI model ID to native Droid model ID.
func MapToNativeModel(proxyModel string) string {
	modelMap := map[string]string{
		"droid-glm-4.6":          "glm-4.6",
		"droid-claude-haiku-4.5": "claude-haiku-4-5-20251001",
		"droid-claude-sonnet-4.5": "claude-sonnet-4-5-20250929",
		"droid-claude-opus-4.5":  "claude-opus-4-5-20251101",
		"droid-gpt-5.1-codex":    "gpt-5.1-codex",
		"droid-gemini-3-pro":     "gemini-3-pro-preview",
		// Also accept native model IDs
		"glm-4.6":                      "glm-4.6",
		"claude-haiku-4-5-20251001":    "claude-haiku-4-5-20251001",
		"claude-sonnet-4-5-20250929":   "claude-sonnet-4-5-20250929",
		"claude-opus-4-5-20251101":     "claude-opus-4-5-20251101",
		"gpt-5.1-codex":                "gpt-5.1-codex",
		"gpt-5-codex":                  "gpt-5-codex",
		"gemini-3-pro-preview":         "gemini-3-pro-preview",
	}

	if native, ok := modelMap[proxyModel]; ok {
		return native
	}
	// Return as-is if not in map (allows using native IDs directly)
	return proxyModel
}
