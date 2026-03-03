package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是 GoTems 的全局配置
type Config struct {
	Version   string         `yaml:"version"`
	Global    GlobalConfig   `yaml:"global"`
	Providers ProvidersConfig `yaml:"providers"`
	Routing   RoutingConfig  `yaml:"routing"`
	Comm      CommConfig     `yaml:"communication"`
}

type GlobalConfig struct {
	LogLevel           string        `yaml:"log_level"`
	MaxConcurrentAgents int          `yaml:"max_concurrent_agents"`
	DefaultTimeout     time.Duration `yaml:"default_timeout"`
}

type ProvidersConfig struct {
	Claude  *ProviderConfig `yaml:"claude,omitempty"`
	Gemini  *ProviderConfig `yaml:"gemini,omitempty"`
	OpenAI  *ProviderConfig `yaml:"openai,omitempty"`
	Ollama  *OllamaConfig   `yaml:"ollama,omitempty"`
}

type ProviderConfig struct {
	APIKey string        `yaml:"api_key"`
	Models []ModelConfig `yaml:"models"`
	CLI    *CLIConfig    `yaml:"cli,omitempty"`
}

type ModelConfig struct {
	ID              string   `yaml:"id"`
	Role            string   `yaml:"role"`
	CostPerKInput   float64  `yaml:"cost_per_1k_input"`
	CostPerKOutput  float64  `yaml:"cost_per_1k_output"`
	Strengths       []string `yaml:"strengths"`
	MaxContext      int      `yaml:"max_context,omitempty"`
}

type CLIConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

type OllamaConfig struct {
	BaseURL string        `yaml:"base_url"`
	Models  []ModelConfig `yaml:"models"`
}

type RoutingConfig struct {
	Strategy  string       `yaml:"strategy"` // best_fit, cost_first, speed_first, consensus
	Fallback  string       `yaml:"fallback"`
	CostLimit *CostLimit   `yaml:"cost_limit,omitempty"`
}

type CostLimit struct {
	Daily   float64 `yaml:"daily"`
	PerTask float64 `yaml:"per_task"`
}

type CommConfig struct {
	Bus  string     `yaml:"bus"` // channel, grpc, nats
	NATS *NATSConfig `yaml:"nats,omitempty"`
	MCP  *MCPConfig  `yaml:"mcp,omitempty"`
}

type NATSConfig struct {
	URL string `yaml:"url"`
}

type MCPConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Transport string `yaml:"transport"` // stdio, sse
}

// Load 从 YAML 文件加载配置
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	data = []byte(os.ExpandEnv(string(data)))

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.setDefaults()
	return &cfg, nil
}

// Default 返回默认配置
func Default() *Config {
	cfg := &Config{
		Version: "1.0",
		Global: GlobalConfig{
			LogLevel:           "info",
			MaxConcurrentAgents: 10,
			DefaultTimeout:     5 * time.Minute,
		},
		Routing: RoutingConfig{
			Strategy: "best_fit",
			Fallback: "claude",
		},
		Comm: CommConfig{
			Bus: "channel",
		},
	}
	cfg.loadEnvProviders()
	return cfg
}

func (c *Config) setDefaults() {
	if c.Global.LogLevel == "" {
		c.Global.LogLevel = "info"
	}
	if c.Global.MaxConcurrentAgents == 0 {
		c.Global.MaxConcurrentAgents = 10
	}
	if c.Global.DefaultTimeout == 0 {
		c.Global.DefaultTimeout = 5 * time.Minute
	}
	if c.Routing.Strategy == "" {
		c.Routing.Strategy = "best_fit"
	}
	if c.Comm.Bus == "" {
		c.Comm.Bus = "channel"
	}
	c.loadEnvProviders()
}

func (c *Config) loadEnvProviders() {
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" && c.Providers.Claude == nil {
		c.Providers.Claude = &ProviderConfig{
			APIKey: key,
			Models: []ModelConfig{
				{ID: "claude-sonnet-4-6", Role: "lead", Strengths: []string{"reasoning", "code_review", "refactoring"}},
			},
		}
	}
	if key := os.Getenv("GOOGLE_API_KEY"); key != "" && c.Providers.Gemini == nil {
		c.Providers.Gemini = &ProviderConfig{
			APIKey: key,
			Models: []ModelConfig{
				{ID: "gemini-2.5-pro", Role: "worker", Strengths: []string{"multimodal", "large_context", "code_generation"}},
			},
		}
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" && c.Providers.OpenAI == nil {
		c.Providers.OpenAI = &ProviderConfig{
			APIKey: key,
			Models: []ModelConfig{
				{ID: "gpt-4o", Role: "worker", Strengths: []string{"code_generation", "test_generation"}},
			},
		}
	}
}
