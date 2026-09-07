package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetLatestProcessedM3UPath(t *testing.T) {
	dir := t.TempDir()
	prev := GetConfig()
	SetConfig(&Config{DataPath: dir, TempPath: dir})
	t.Cleanup(func() { SetConfig(prev) })
	procDir := GetProcessedDirPath()
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := GetLatestProcessedM3UPath()
	if err == nil || got != "" {
		t.Fatalf("empty dir: want error, got %q err %v", got, err)
	}

	for _, name := range []string{"20240101010101.m3u", "20240102020202.m3u", "20240103030303.tmp"} {
		if err := os.WriteFile(filepath.Join(procDir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err = GetLatestProcessedM3UPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "20240102020202.m3u" {
		t.Fatalf("want latest non-tmp file, got %q", got)
	}

	if err := os.Remove(filepath.Join(procDir, "20240101010101.m3u")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(procDir, "20240102020202.m3u")); err != nil {
		t.Fatal(err)
	}

	got, err = GetLatestProcessedM3UPath()
	if err == nil || got != "" {
		t.Fatalf("only-tmp dir: want error, got %q err %v", got, err)
	}
}
