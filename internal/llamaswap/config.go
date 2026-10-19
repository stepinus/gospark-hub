package llamaswap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// ModelConfig is the part of a llama-swap model entry the Hub shows and edits.
type ModelConfig struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	Proxy       string `yaml:"proxy" json:"proxy"`
	Cmd         string `yaml:"cmd" json:"cmd"`
	TTL         int    `yaml:"ttl" json:"ttl"`
}

// GroupConfig is a llama-swap model group: swap and exclusive control whether
// members evict each other, persistent keeps the group loaded.
type GroupConfig struct {
	Swap       *bool    `yaml:"swap" json:"swap"`
	Exclusive  *bool    `yaml:"exclusive" json:"exclusive"`
	Persistent *bool    `yaml:"persistent" json:"persistent"`
	Members    []string `yaml:"members" json:"members"`
}

// Config is the whole file, as far as the Hub understands it. Everything else
// in the YAML is preserved untouched when groups are rewritten.
type Config struct {
	Models map[string]ModelConfig `yaml:"models" json:"models"`
	Groups map[string]GroupConfig `yaml:"groups" json:"groups"`
}

// ConfigYAML returns the raw config text the editor shows.
func (m *Manager) ConfigYAML() (string, error) {
	data, err := os.ReadFile(m.ConfigPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrNoConfig
		}
		return "", err
	}
	return string(data), nil
}

// ErrNoConfig means the sidecar has no config to serve yet.
var ErrNoConfig = errors.New("llama-swap config does not exist")

// SaveConfigYAML replaces the whole config. The text must parse and its group
// members must name models that exist; llama-swap validates the rest itself,
// and -watch-config reloads the file in place.
func (m *Manager) SaveConfigYAML(text string) error {
	cfg, err := ParseConfig([]byte(text))
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	return writeFileAtomic(m.ConfigPath(), []byte(text))
}

// ParseConfig unmarshals the YAML the Hub cares about.
func ParseConfig(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

// Validate rejects group members that name no model. An empty group is
// allowed: it is how a user parks a group before filling it.
func (c *Config) Validate() error {
	for group, g := range c.Groups {
		for _, member := range g.Members {
			if _, ok := c.Models[member]; !ok {
				return fmt.Errorf("group %q lists unknown model %q", group, member)
			}
		}
	}
	return nil
}

// Config reads and parses the managed config.
func (m *Manager) Config() (*Config, error) {
	text, err := m.ConfigYAML()
	if err != nil {
		return nil, err
	}
	return ParseConfig([]byte(text))
}

// SetGroups rewrites only the top-level `groups:` key, so comments, model
// definitions and router settings elsewhere in the file survive. The result is
// re-read and validated before it is written.
func (m *Manager) SetGroups(groups map[string]GroupConfig) error {
	text, err := m.ConfigYAML()
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if len(doc.Content) == 0 {
		return errors.New("config is empty")
	}
	root := doc.Content[0]
	cfg, err := ParseConfig([]byte(text))
	if err != nil {
		return err
	}
	cfg.Groups = groups
	if err := cfg.Validate(); err != nil {
		return err
	}

	// Build the replacement node from an ordered mapping so the file reads the
	// same way every save.
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "groups"}
	value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, name := range names {
		g := groups[name]
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		addBool(node, "swap", g.Swap)
		addBool(node, "exclusive", g.Exclusive)
		addBool(node, "persistent", g.Persistent)
		members := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, member := range g.Members {
			members.Content = append(members.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: member})
		}
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "members"}, members)
		value.Content = append(value.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, node)
	}

	replaced := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "groups" {
			root.Content[i+1] = value
			replaced = true
			break
		}
	}
	if !replaced {
		root.Content = append(root.Content, key, value)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	if err := m.SaveConfigYAML(string(out)); err != nil {
		return err
	}
	return nil
}

// addBool writes only flags the config actually sets, so a group does not grow
// keys nobody asked for.
func addBool(node *yaml.Node, name string, value *bool) {
	if value == nil {
		return
	}
	text := "false"
	if *value {
		text = "true"
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: text})
}

// SeedConfig copies an existing llama-swap config into the managed home the
// first time the sidecar is installed, so a machine that already ran
// llama-swap keeps its models.
func (m *Manager) SeedConfig(from string) (bool, error) {
	if _, err := os.Stat(m.ConfigPath()); err == nil {
		return false, nil
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return false, err
	}
	if _, err := ParseConfig(data); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(m.ConfigPath()), 0o755); err != nil {
		return false, err
	}
	return true, writeFileAtomic(m.ConfigPath(), data)
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
