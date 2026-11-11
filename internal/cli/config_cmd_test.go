package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/peakacom/peaka-cli/internal/config"
)

// config set/get/list keep classifier-key in the secret store, out of the yaml, and never print it raw.
func TestConfigClassifierKeyStaysOutOfTheFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PEAKA_CONFIG_DIR", dir)
	t.Setenv("PEAKA_CREDENTIALS_STORE", "file")
	t.Setenv("PEAKA_CLASSIFIER_API_KEY", "")
	old := rt
	defer func() { rt = old }()
	run := func(args ...string) string {
		var out bytes.Buffer
		c := &config.Configuration{Name: "work"}
		rt = &Runtime{Ctx: context.Background(), Config: c, Settings: config.Resolve(c, config.Overrides{}), Out: &out, Err: io.Discard}
		rt.Settings.Format = "json"
		cmd := newConfigCmd()
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out.String()
	}

	run("set", "classifier-key", "sk-live-1234567890")
	if b, _ := os.ReadFile(filepath.Join(dir, "configurations", "work.yaml")); strings.Contains(string(b), "sk-live") {
		t.Fatalf("key written to the configuration file: %s", b)
	}
	if k, src, err := config.ClassifierKey("work"); err != nil || k != "sk-live-1234567890" || src != "file" {
		t.Fatalf("stored key: %q %q %v", k, src, err)
	}
	for _, args := range [][]string{{"get", "classifier-key"}, {"list"}} {
		out := run(args...)
		if strings.Contains(out, "sk-live-1234567890") {
			t.Fatalf("%v printed the raw key: %s", args, out)
		}
		if !strings.Contains(out, config.MaskSecret("sk-live-1234567890")) || !strings.Contains(out, `"file"`) {
			t.Fatalf("%v should show the masked key with its source: %s", args, out)
		}
	}
	run("unset", "classifier-key")
	if k, _, _ := config.ClassifierKey("work"); k != "" {
		t.Fatalf("unset left the key: %q", k)
	}
}
