package secretmem_test

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if memoryGuardChild() {
		return
	}
	os.Exit(m.Run())
}
