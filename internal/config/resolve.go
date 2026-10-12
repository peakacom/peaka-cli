package config

import (
	"net/url"
	"os"
	"strings"
)

// Settings are the effective values for one invocation, each with its source.
type Settings struct {
	Configuration string
	Environment   string
	API           string
	DBC           string
	OAuth         string
	Account       string
	Org           string
	Project       string
	Format        string
	Color         string
	Pager         string

	// Source says where each property came from: flag, env, config, preset, default.
	Source map[string]string
}

// Overrides are the values given on the command line ("" = not given).
type Overrides struct {
	Environment, API, DBC, OAuth, Account, Org, Project, Format string
}

// envVar maps a property to its environment variable.
var envVar = map[string]string{
	"environment": "PEAKA_ENVIRONMENT",
	"api":         "PEAKA_API_URL",
	"dbc":         "PEAKA_DBC_URL",
	"oauth":       "PEAKA_OAUTH_URL",
	"account":     "PEAKA_ACCOUNT",
	"org":         "PEAKA_ORG",
	"project":     "PEAKA_PROJECT",
	"format":      "PEAKA_FORMAT",
	"color":       "PEAKA_COLOR",
	"pager":       "PEAKA_PAGER",
}

// Resolve applies flag > env > configuration > preset > default for every property.
func Resolve(c *Configuration, o Overrides) *Settings {
	s := &Settings{Configuration: c.Name, Source: map[string]string{}}
	pick := func(key, flag string) string {
		if flag != "" {
			s.Source[key] = "flag"
			return flag
		}
		if v := os.Getenv(envVar[key]); v != "" {
			s.Source[key] = "env"
			return v
		}
		if v, _ := c.Get(key); v != "" {
			s.Source[key] = "config"
			return v
		}
		return ""
	}
	s.Environment = pick("environment", o.Environment)
	if s.Environment == "" {
		s.Environment, s.Source["environment"] = DefaultEnvironment, "default"
	}
	preset := Environments[s.Environment]
	s.API = pick("api", o.API)
	if s.API == "" {
		s.API, s.Source["api"] = preset.API, "preset("+s.Environment+")"
	}
	s.DBC = pick("dbc", o.DBC)
	if s.DBC == "" {
		s.DBC, s.Source["dbc"] = preset.DBC, "preset("+s.Environment+")"
	}
	s.OAuth = pick("oauth", o.OAuth)
	if s.OAuth == "" {
		s.OAuth, s.Source["oauth"] = preset.OAuth, "preset("+s.Environment+")"
	}
	s.Account = pick("account", o.Account)
	s.Org = pick("org", o.Org)
	s.Project = pick("project", o.Project)
	s.Format = pick("format", o.Format)
	s.Color = pick("color", "")
	if s.Color == "" {
		s.Color, s.Source["color"] = "auto", "default"
	}
	s.Pager = pick("pager", "")
	return s
}

// APIHost is the host part of the API URL; credentials are keyed by it.
func (s *Settings) APIHost() string {
	u, err := url.Parse(s.API)
	if err != nil || u.Host == "" {
		return strings.TrimPrefix(strings.TrimPrefix(s.API, "https://"), "http://")
	}
	return u.Host
}

// Values returns the properties as a map (for `config list`).
func (s *Settings) Values() map[string]string {
	return map[string]string{
		"environment": s.Environment, "api": s.API, "dbc": s.DBC, "oauth": s.OAuth, "account": s.Account,
		"org": s.Org, "project": s.Project, "format": s.Format, "color": s.Color, "pager": s.Pager,
	}
}
