package release

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0}, {"v1.2.3", "1.2.3", 0}, {"1.2.3", "1.2.4", -1}, {"1.10.0", "1.9.9", 1},
		{"1.2.3-rc1", "1.2.3", -1}, {"1.2.3", "1.2.3-rc1", 1}, {"1.2.3-rc1", "1.2.3-rc2", -1},
		{"dev", "0.1.0", -1}, {"dc89cf4-dirty", "0.1.0", -1}, {"0.1.0", "dev", 1},
	}
	for _, c := range cases {
		got := Compare(c.a, c.b)
		if (got < 0) != (c.want < 0) || (got > 0) != (c.want > 0) {
			t.Errorf("Compare(%q,%q) = %d, want sign of %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIsRelease(t *testing.T) {
	for v, want := range map[string]bool{
		"1.2.3": true, "v1.2.3": true, "1.2.3-rc1": true,
		"dev": false, "dc89cf4": false, "v0.1.0-3-gabc1234": false, "v0.1.0-dirty": false, "0.0.1-next": false,
	} {
		if IsRelease(v) != want {
			t.Errorf("IsRelease(%q) = %v", v, !want)
		}
	}
}

func TestChecksumsAndVerify(t *testing.T) {
	data := []byte("binary")
	sum := sha256.Sum256(data)
	sums := Checksums([]byte(hex.EncodeToString(sum[:]) + "  peaka_1.0.0_linux_amd64.tar.gz\nabc  other\n"))
	if err := Verify("peaka_1.0.0_linux_amd64.tar.gz", data, sums); err != nil {
		t.Fatal(err)
	}
	if err := Verify("peaka_1.0.0_linux_amd64.tar.gz", []byte("tampered"), sums); err == nil {
		t.Fatal("a tampered archive verified")
	}
	if err := Verify("missing", data, sums); err == nil {
		t.Fatal("an unlisted archive verified")
	}
}

func TestBinaryFromArchives(t *testing.T) {
	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"README.md": "readme", "peaka": "ELF"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	got, err := Binary("peaka_1.0.0_linux_amd64.tar.gz", tgz.Bytes())
	if err != nil || string(got) != "ELF" {
		t.Fatalf("tar.gz: %q %v", got, err)
	}

	var z bytes.Buffer
	zw := zip.NewWriter(&z)
	w, _ := zw.Create("peaka.exe")
	_, _ = w.Write([]byte("MZ"))
	zw.Close()
	got, err = Binary("peaka_1.0.0_windows_amd64.zip", z.Bytes())
	if err != nil || string(got) != "MZ" {
		t.Fatalf("zip: %q %v", got, err)
	}
}

func TestInstallReplacesTheFile(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "peaka")
	_ = os.WriteFile(exe, []byte("old"), 0o755)
	if err := Install(exe, []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "new" {
		t.Fatalf("got %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestArchiveName(t *testing.T) {
	if ArchiveName("1.2.3", "darwin", "arm64") != "peaka_1.2.3_darwin_arm64.tar.gz" {
		t.Fatal(ArchiveName("1.2.3", "darwin", "arm64"))
	}
	if ArchiveName("1.2.3", "windows", "amd64") != "peaka_1.2.3_windows_amd64.zip" {
		t.Fatal(ArchiveName("1.2.3", "windows", "amd64"))
	}
}
