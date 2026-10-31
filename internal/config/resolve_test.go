package config

import "testing"

func TestResolvePrecedence(t *testing.T) {
	t.Setenv("PEAKA_PROJECT", "from-env")
	t.Setenv("PEAKA_ENVIRONMENT", "")
	t.Setenv("PEAKA_API_URL", "")
	t.Setenv("PEAKA_DBC_URL", "")
	t.Setenv("PEAKA_ORG", "")
	t.Setenv("PEAKA_ACCOUNT", "")
	t.Setenv("PEAKA_FORMAT", "")
	c := &Configuration{Name: "x", Environment: "test", Project: "from-config", Org: "org-config", DBC: "https://sql.local:4567"}

	s := Resolve(c, Overrides{Org: "org-flag"})
	if s.Org != "org-flag" || s.Source["org"] != "flag" {
		t.Fatalf("flag should win: %+v", s)
	}
	if s.Project != "from-env" || s.Source["project"] != "env" {
		t.Fatalf("env should beat config: %+v", s)
	}
	if s.API != Environments["test"].API || s.Source["api"] != "preset(test)" {
		t.Fatalf("api should come from the preset: %q %q", s.API, s.Source["api"])
	}
	if s.DBC != "https://sql.local:4567" || s.Source["dbc"] != "config" {
		t.Fatalf("dbc override in config should win over the preset: %q %q", s.DBC, s.Source["dbc"])
	}
	if s.APIHost() != "api-test.peaka.host" {
		t.Fatalf("host: %q", s.APIHost())
	}
}

func TestResolveDefaults(t *testing.T) {
	for _, k := range envVar {
		t.Setenv(k, "")
	}
	s := Resolve(&Configuration{Name: DefaultName}, Overrides{})
	if s.Environment != "prod" || s.Source["environment"] != "default" {
		t.Fatalf("default environment: %+v", s)
	}
	if s.API != Environments["prod"].API || s.DBC != Environments["prod"].DBC {
		t.Fatalf("prod preset: %q %q", s.API, s.DBC)
	}
}

func TestSetValidation(t *testing.T) {
	c := &Configuration{Name: "x"}
	if err := c.Set("environment", "mars"); err == nil {
		t.Fatal("unknown environment accepted")
	}
	if err := c.Set("api", "ftp://x"); err == nil {
		t.Fatal("non-http url accepted")
	}
	if err := c.Set("api", "https://x/api/v1/"); err != nil || c.API != "https://x/api/v1" {
		t.Fatalf("trailing slash should be trimmed: %v %q", err, c.API)
	}
	if err := c.Set("format", "xml"); err == nil {
		t.Fatal("unknown format accepted")
	}
	if err := c.Set("nope", "1"); err == nil {
		t.Fatal("unknown property accepted")
	}
}

func TestConfigurationsOnDisk(t *testing.T) {
	t.Setenv("PEAKA_CONFIG_DIR", t.TempDir())
	t.Setenv("PEAKA_CONFIGURATION", "")
	if _, err := Create("work"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create("work"); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err := Create("Bad Name"); err == nil {
		t.Fatal("bad name accepted")
	}
	if err := SetActive("work"); err != nil {
		t.Fatal(err)
	}
	if ActiveName() != "work" {
		t.Fatalf("active: %q", ActiveName())
	}
	if err := Delete("work"); err == nil {
		t.Fatal("active configuration deleted")
	}
	names, _ := List()
	if len(names) != 2 || names[0] != "default" || names[1] != "work" {
		t.Fatalf("list: %v", names)
	}
	c, err := Load("default")
	if err != nil || c.Name != "default" {
		t.Fatalf("default should load empty: %v %+v", err, c)
	}
}

func TestDefaultConfigurationCanBeActivatedWithoutAFile(t *testing.T) {
	t.Setenv("PEAKA_CONFIG_DIR", t.TempDir())
	t.Setenv("PEAKA_CONFIGURATION", "")
	if _, err := Create("e2e"); err != nil {
		t.Fatal(err)
	}
	if err := SetActive("e2e"); err != nil {
		t.Fatal(err)
	}
	if err := SetActive(DefaultName); err != nil || ActiveName() != DefaultName {
		t.Fatalf("back to default: %v, active %q", err, ActiveName())
	}
	if err := SetActive("missing"); err == nil {
		t.Fatal("a missing configuration was activated")
	}
}
