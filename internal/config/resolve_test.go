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
	t.Setenv("PEAKA_CLASSIFIER_URL", "")
	t.Setenv("PEAKA_CLASSIFIER_MODEL", "")
	t.Setenv("PEAKA_CLASSIFIER_TEMPLATE", "/env/template.txt")
	c := &Configuration{Name: "x", Environment: "test", Project: "from-config", Org: "org-config", DBC: "https://sql.local:4567",
		ClassifierURL: "https://classifier.local", ClassifierModel: "model-config", ClassifierTemplate: "/config/template.txt"}

	s := Resolve(c, Overrides{Org: "org-flag", ClassifierURL: "https://classifier.flag"})
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
	if s.ClassifierURL != "https://classifier.flag" || s.Source["classifier-url"] != "flag" {
		t.Fatalf("classifier-url flag should win: %q %q", s.ClassifierURL, s.Source["classifier-url"])
	}
	if s.ClassifierModel != "model-config" || s.Source["classifier-model"] != "config" {
		t.Fatalf("classifier-model should come from the config: %q %q", s.ClassifierModel, s.Source["classifier-model"])
	}
	if s.ClassifierTemplate != "/env/template.txt" || s.Source["classifier-template"] != "env" {
		t.Fatalf("classifier-template env should beat config: %q %q", s.ClassifierTemplate, s.Source["classifier-template"])
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
	if s.ClassifierURL != DefaultClassifierURL || s.Source["classifier-url"] != "default" {
		t.Fatalf("default classifier-url: %q %q", s.ClassifierURL, s.Source["classifier-url"])
	}
	if s.ClassifierModel != DefaultClassifierModel || s.Source["classifier-model"] != "default" {
		t.Fatalf("default classifier-model: %q %q", s.ClassifierModel, s.Source["classifier-model"])
	}
	if s.ClassifierTemplate != "" || s.Source["classifier-template"] != "" {
		t.Fatalf("classifier-template has no default: %q %q", s.ClassifierTemplate, s.Source["classifier-template"])
	}
	if _, ok := s.Values()["classifier-key"]; ok {
		t.Fatal("classifier-key is a secret and must not be in Values()")
	}
	for _, p := range Properties {
		if _, ok := s.Values()[p]; !ok && p != "classifier-key" {
			t.Fatalf("property %q missing from Values()", p)
		}
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
	if err := c.Set("classifier-url", "ftp://x"); err == nil {
		t.Fatal("non-http classifier url accepted")
	}
	if err := c.Set("classifier-url", "https://x/v1/"); err != nil || c.ClassifierURL != "https://x/v1" {
		t.Fatalf("classifier-url trailing slash should be trimmed: %v %q", err, c.ClassifierURL)
	}
	if err := c.Set("classifier-key", "secret"); err == nil {
		t.Fatal("classifier-key written to the configuration")
	}
	if v, err := c.Get("classifier-key"); err != nil || v != "" {
		t.Fatalf("classifier-key must never be read from the configuration: %v %q", err, v)
	}
}

func TestClassifierKey(t *testing.T) {
	t.Setenv("PEAKA_CREDENTIALS_STORE", "file")
	t.Setenv("PEAKA_CONFIG_DIR", t.TempDir())
	t.Setenv("PEAKA_CLASSIFIER_API_KEY", "")
	if k, src, err := ClassifierKey("work"); err != nil || k != "" || src != "" {
		t.Fatalf("none stored: %q %q %v", k, src, err)
	}
	if err := SetClassifierKey("work", "sk-stored"); err != nil {
		t.Fatal(err)
	}
	if k, src, err := ClassifierKey("work"); err != nil || k != "sk-stored" || src != "file" {
		t.Fatalf("stored key: %q %q %v", k, src, err)
	}
	if k, _, _ := ClassifierKey("other"); k != "" {
		t.Fatalf("keys are per configuration: %q", k)
	}
	c, _ := Load("work")
	if v, _ := c.Get("classifier-key"); v != "" {
		t.Fatalf("key leaked into the configuration: %q", v)
	}
	t.Setenv("PEAKA_CLASSIFIER_API_KEY", "sk-env")
	if k, src, err := ClassifierKey("work"); err != nil || k != "sk-env" || src != "env" {
		t.Fatalf("env should win: %q %q %v", k, src, err)
	}
	t.Setenv("PEAKA_CLASSIFIER_API_KEY", "")
	if err := SetClassifierKey("work", ""); err != nil {
		t.Fatal(err)
	}
	if k, src, err := ClassifierKey("work"); err != nil || k != "" || src != "" {
		t.Fatalf("deleted: %q %q %v", k, src, err)
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
