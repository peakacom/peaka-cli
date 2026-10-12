// Package config stores named configurations (gcloud style) and resolves the
// effective settings from flags, environment variables, the active
// configuration and the environment preset, remembering where each came from.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Configuration is one named profile on disk.
type Configuration struct {
	Name        string `yaml:"-"`
	Environment string `yaml:"environment,omitempty"`
	API         string `yaml:"api,omitempty"`
	DBC         string `yaml:"dbc,omitempty"`
	Account     string `yaml:"account,omitempty"`
	Org         string `yaml:"org,omitempty"`
	Project     string `yaml:"project,omitempty"`
	Format      string `yaml:"format,omitempty"`
	Color       string `yaml:"color,omitempty"`
	Pager       string `yaml:"pager,omitempty"`
}

// Properties lists every settable key, in display order.
var Properties = []string{"environment", "api", "dbc", "account", "org", "project", "format", "color", "pager"}

const DefaultName = "default"

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Dir is the configuration directory: $PEAKA_CONFIG_DIR, else $XDG_CONFIG_HOME/peaka, else ~/.config/peaka.
func Dir() string {
	if d := os.Getenv("PEAKA_CONFIG_DIR"); d != "" {
		return d
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "peaka")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".peaka"
	}
	return filepath.Join(home, ".config", "peaka")
}

func configurationsDir() string { return filepath.Join(Dir(), "configurations") }
func pathOf(name string) string { return filepath.Join(configurationsDir(), name+".yaml") }
func activePath() string        { return filepath.Join(Dir(), "active_config") }

// ActiveName returns the active configuration's name ($PEAKA_CONFIGURATION wins).
func ActiveName() string {
	if n := os.Getenv("PEAKA_CONFIGURATION"); n != "" {
		return n
	}
	b, err := os.ReadFile(activePath())
	if err != nil {
		return DefaultName
	}
	if n := strings.TrimSpace(string(b)); n != "" {
		return n
	}
	return DefaultName
}

// SetActive makes name the active configuration; it must exist.
func SetActive(name string) error {
	if !Exists(name) {
		return fmt.Errorf("configuration %q does not exist", name)
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(activePath(), []byte(name+"\n"), 0o600)
}

func Exists(name string) bool {
	_, err := os.Stat(pathOf(name))
	return err == nil
}

// Load reads a configuration; a missing "default" is an empty one, any other missing name is an error.
func Load(name string) (*Configuration, error) {
	c := &Configuration{Name: name}
	b, err := os.ReadFile(pathOf(name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if name == DefaultName {
				return c, nil
			}
			return nil, fmt.Errorf("configuration %q does not exist (peaka config configurations list)", name)
		}
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", pathOf(name), err)
	}
	c.Name = name
	return c, nil
}

// Save writes the configuration file (0600).
func Save(c *Configuration) error {
	if err := os.MkdirAll(configurationsDir(), 0o700); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(pathOf(c.Name), b, 0o600)
}

// Create makes a new, empty configuration.
func Create(name string) (*Configuration, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("configuration name %q: use lowercase letters, digits, '-' or '_'", name)
	}
	if Exists(name) {
		return nil, fmt.Errorf("configuration %q already exists", name)
	}
	c := &Configuration{Name: name}
	return c, Save(c)
}

// Delete removes a configuration; the active one cannot be deleted.
func Delete(name string) error {
	if name == ActiveName() {
		return fmt.Errorf("configuration %q is active; activate another one first", name)
	}
	if !Exists(name) {
		return fmt.Errorf("configuration %q does not exist", name)
	}
	return os.Remove(pathOf(name))
}

// List returns every configuration name, "default" included even when it has no file.
func List() ([]string, error) {
	entries, err := os.ReadDir(configurationsDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	seen := map[string]bool{DefaultName: true}
	for _, e := range entries {
		n := strings.TrimSuffix(e.Name(), ".yaml")
		if n != e.Name() {
			seen[n] = true
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// Get returns a property's stored value ("" when unset).
func (c *Configuration) Get(key string) (string, error) {
	switch key {
	case "environment":
		return c.Environment, nil
	case "api":
		return c.API, nil
	case "dbc":
		return c.DBC, nil
	case "account":
		return c.Account, nil
	case "org":
		return c.Org, nil
	case "project":
		return c.Project, nil
	case "format":
		return c.Format, nil
	case "color":
		return c.Color, nil
	case "pager":
		return c.Pager, nil
	}
	return "", unknownProperty(key)
}

// Set validates and stores a property value; "" unsets it.
func (c *Configuration) Set(key, value string) error {
	switch key {
	case "environment":
		if value != "" {
			if _, ok := Environments[value]; !ok {
				return fmt.Errorf("environment %q: one of %s", value, strings.Join(EnvironmentNames(), ", "))
			}
		}
		c.Environment = value
	case "api", "dbc":
		if value != "" && !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
			return fmt.Errorf("%s: a URL starting with https:// (or http:// for local use)", key)
		}
		value = strings.TrimRight(value, "/")
		if key == "api" {
			c.API = value
		} else {
			c.DBC = value
		}
	case "account":
		c.Account = value
	case "org":
		c.Org = value
	case "project":
		c.Project = value
	case "format":
		if value != "" && !ValidFormat(value) {
			return fmt.Errorf("format %q: one of %s", value, strings.Join(Formats, ", "))
		}
		c.Format = value
	case "color":
		if value != "" && value != "auto" && value != "always" && value != "never" {
			return fmt.Errorf("color %q: auto, always or never", value)
		}
		c.Color = value
	case "pager":
		c.Pager = value
	default:
		return unknownProperty(key)
	}
	return nil
}

func unknownProperty(key string) error {
	return fmt.Errorf("unknown property %q: one of %s", key, strings.Join(Properties, ", "))
}

// Formats are the output formats every command accepts.
var Formats = []string{"table", "json", "jsonl", "csv", "tsv", "yaml", "md", "vertical"}

func ValidFormat(f string) bool {
	for _, x := range Formats {
		if x == f {
			return true
		}
	}
	return false
}
