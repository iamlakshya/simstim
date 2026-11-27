package vmneed

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

func TestPurgeReaderCommandLinePreservesOriginalVerityTables(t *testing.T) {
	tools := supply.Item{DataBlocks: 123, RootHash: strings.Repeat("a", 64), Salt: strings.Repeat("b", 64)}
	root := image.Request{DataBlocks: 456, RootHash: strings.Repeat("c", 64)}
	ordinary := KernelCommandLine(tools, root)
	reader, err := PurgeReaderKernelCommandLineV1(tools, root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSuffix(reader, " -- wall-init --purge-reader-v1") != strings.TrimSuffix(ordinary, " -- wall-init --microvm-v1") || !strings.HasSuffix(reader, " -- wall-init --purge-reader-v1") {
		t.Fatal("the reader changed original root/tool geometry or lost its closed entry")
	}
	if strings.Contains(reader, "--microvm-v1") || !strings.Contains(reader, "init=/init") {
		t.Fatal("ordinary prepare can select the fixed reader entry")
	}
}
