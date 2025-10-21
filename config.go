package main

import (
	"io/ioutil"
	"os"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// Config holds runtime configuration for the bot
type Config struct {
	DiscordToken   string   `yaml:"discord_token"`
	ForumParentIDs []string `yaml:"forum_parent_ids"`
	// Optional: list of role IDs that are allowed to run commands. If set, users must have at least one of these roles.
	AllowedRoleIDs []string `yaml:"allowed_role_ids"`
	// Optional: list of permission names that are allowed to run commands. Examples: ADMINISTRATOR, MANAGE_CHANNELS, MANAGE_MESSAGES
	AllowedPermissions []string `yaml:"allowed_permissions"`
}

// LoadConfig reads config.yaml if present and merges with environment variables (env overrides file)
func LoadConfig(path string) (*Config, error) {
	cfg := &Config{}
	if _, err := os.Stat(path); err == nil {
		b, err := ioutil.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(b, cfg); err != nil {
			return nil, err
		}
	}

	// env overrides
	if t := os.Getenv("DISCORD_TOKEN"); t != "" {
		cfg.DiscordToken = t
	}
	if p := os.Getenv("FORUM_PARENT_IDS"); p != "" {
		// comma-separated
		parts := []string{}
		for _, v := range strings.Split(p, ",") {
			parts = append(parts, strings.TrimSpace(v))
		}
		cfg.ForumParentIDs = parts
	}

	if r := os.Getenv("ALLOWED_ROLE_IDS"); r != "" {
		parts := []string{}
		for _, v := range strings.Split(r, ",") {
			parts = append(parts, strings.TrimSpace(v))
		}
		cfg.AllowedRoleIDs = parts
	}
	if p := os.Getenv("ALLOWED_PERMISSIONS"); p != "" {
		parts := []string{}
		for _, v := range strings.Split(p, ",") {
			parts = append(parts, strings.TrimSpace(v))
		}
		cfg.AllowedPermissions = parts
	}

	return cfg, nil
}
