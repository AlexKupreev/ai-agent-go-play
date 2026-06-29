package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// Config holds persistent settings stored on disk.
type Config struct {
	OpenAIKey string `json:"openai_key"`
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage agent configuration",
}

var setKeyCmd = &cobra.Command{
	Use:   "set-key <api-key>",
	Short: "Save your OpenAI API key",
	Args:  cobra.ExactArgs(1), // enforces exactly one argument
	RunE: func(cmd *cobra.Command, args []string) error {
		return saveConfig(Config{OpenAIKey: args[0]})
	},
}

func init() {
	configCmd.AddCommand(setKeyCmd)
}

// configPath returns the path to the config file, e.g. ~/.config/ai-agent/config.json
func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ai-agent", "config.json"), nil
}

// catalogPath returns the path to the persistent tool catalog, e.g.
// ~/.config/ai-agent/tools.json (created on first authored tool).
func catalogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ai-agent", "tools.json"), nil
}

func saveConfig(cfg Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	// Create the directory if it doesn't exist. 0700 = only owner can read/write/enter.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 0600 = only owner can read/write. Important: this file contains your API key.
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	fmt.Println("API key saved to", path)
	return nil
}

func loadConfig() (Config, error) {
	path, err := configPath()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("no config found — run: agent config set-key <your-key>")
	}
	var cfg Config
	return cfg, json.Unmarshal(data, &cfg)
}
