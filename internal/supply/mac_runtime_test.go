package supply

import (
	"strings"
	"testing"
)

func TestMacRuntimeLockKeepsExactPublicByteTuples(t *testing.T) {
	text := `version = 1
[lima]
name = "lima"
version = "2.3.0"
arch = "arm64"
url = "https://github.com/lima-vm/lima/releases/download/v2.3.0/lima-2.3.0-Darwin-arm64.tar.gz"
sha256 = "` + strings.Repeat("a", 64) + `"
size = 7
asset = "lima-2.3.0-Darwin-arm64.tar.gz"
[image]
name = "lima-ubuntu"
version = "26.04-20261016"
arch = "arm64"
url = "https://cloud-images.ubuntu.com/releases/resolute/release-20261016/ubuntu-26.04-server-cloudimg-arm64.img"
sha256 = "` + strings.Repeat("b", 64) + `"
size = 5
asset = "lima-ubuntu-26.04-20261016-arm64.img"
`
	lock, err := ReadMacRuntimeLockV1(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	items, err := MacRuntimeManifestItemsV1(lock, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].SHA256 != lock.Lima.SHA256 || items[1].SHA256 != lock.Image.SHA256 || items[0].URL != ReleaseFolder("1.0.0")+"/"+lock.Lima.Asset {
		t.Fatal("mirroring changed signed byte identities")
	}
	if _, _, err := MacRuntimeItemsV1(Manifest{Release: "1.0.0", Items: items}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(text, "2.3.0", "2.2.0", -1), text + "unknown = true\n", strings.Replace(text, "arch = \"arm64\"", "arch = \"amd64\"", 1), strings.Replace(text, "https://cloud-images.ubuntu.com", "http://cloud-images.ubuntu.com", 1)} {
		if _, err := ReadMacRuntimeLockV1(strings.NewReader(bad)); err == nil {
			t.Fatal("invalid Mac pin source accepted")
		}
	}
}
