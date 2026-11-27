package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProviderMetadataResolverUsesOnlyExplicitAbsolutePATH(t *testing.T) {
	directory := t.TempDir()
	leaf := "docker"
	if runtime.GOOS == "windows" {
		leaf += ".exe"
	}
	selected := filepath.Join(directory, leaf)
	if err := os.WriteFile(selected, []byte("owned resolver fixture; never executed"), 0700); err != nil {
		t.Fatal(err)
	}
	environment := []string{"PATH=relative" + string(os.PathListSeparator) + directory}
	if runtime.GOOS == "windows" {
		environment = append(environment, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"))
	}
	t.Setenv("PATH", t.TempDir())
	got, err := resolveProviderEnvironment("docker", environment)
	if err != nil || got != selected {
		t.Fatalf("explicit lookup = %q %v", got, err)
	}
	if got, err := resolveProviderEnvironment("docker", append([]string(nil), environment[:0]...)); err == nil || got != "" {
		t.Fatal("missing explicit metadata inherited ambient lookup")
	}
	if err := os.Remove(selected); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if err := os.WriteFile(selected+".com", []byte("extension bait; never executed"), 0700); err != nil {
			t.Fatal(err)
		}
		if got, err := resolveProviderEnvironment(selected, environment); err == nil || got != "" {
			t.Fatal("disappeared .exe acquired an extension fallback")
		}
	}
}
