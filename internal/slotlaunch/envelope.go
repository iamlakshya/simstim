package slotlaunch

import (
	"bytes"
	"crypto/sha256"
	_ "crypto/sha512" // OpenSSH SSHSIG uses SHA-512 by default.
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/hiddeco/sshsig"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/crypto/ssh"
)

// ReleasePublicKey is the decision 0017 release key. A test keeps it equal
// to .github/release-signers; no request or environment can select a key.
const ReleasePublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGyv3BRbYeIpUvqwia5q4CuqngP+aUzouWZ8x73U2LOy"

func SignatureNamespace() string { return brand.Name + "-launch" }

// Envelope binds one exact executable to its role, architecture and version.
type Envelope struct {
	Role    string `json:"role"`
	Arch    string `json:"arch"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

var semverRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

func releaseSemver(value string) bool {
	match := semverRE.FindStringSubmatch(value)
	if match == nil {
		return false
	}
	for _, part := range strings.Split(match[4], ".") {
		if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
			return false
		}
	}
	return true
}

func compareNumber(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

func compareVersion(a, b string) int {
	x, y := semverRE.FindStringSubmatch(a), semverRE.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		if c := compareNumber(x[i], y[i]); c != 0 {
			return c
		}
	}
	if x[4] == y[4] {
		return 0
	}
	if x[4] == "" {
		return 1
	}
	if y[4] == "" {
		return -1
	}
	xp, yp := strings.Split(x[4], "."), strings.Split(y[4], ".")
	for i := 0; i < len(xp) && i < len(yp); i++ {
		xn, yn := strings.Trim(xp[i], "0123456789") == "", strings.Trim(yp[i], "0123456789") == ""
		var c int
		switch {
		case xn && yn:
			c = compareNumber(xp[i], yp[i])
		case xn:
			c = -1
		case yn:
			c = 1
		default:
			c = strings.Compare(xp[i], yp[i])
		}
		if c != 0 {
			return c
		}
	}
	return compareNumber(strconv.Itoa(len(xp)), strconv.Itoa(len(yp)))
}

// MakeEnvelope hashes the actual bounded release executable. It never signs.
func MakeEnvelope(binary io.Reader, arch, version string) ([]byte, error) {
	if arch != "amd64" && arch != "arm64" || !releaseSemver(version) {
		return nil, refused("the launcher release architecture or semantic version is invalid")
	}
	input, err := io.ReadAll(io.LimitReader(binary, (256<<20)+1))
	if err != nil || len(input) == 0 || len(input) > 256<<20 {
		return nil, refused("the launcher executable is empty, oversized or unreadable")
	}
	if err := checkExecutable(bytes.NewReader(input), arch); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(input)
	data, err := json.Marshal(Envelope{Role: "launch", Arch: arch, Version: version, SHA256: hex.EncodeToString(hash[:])})
	return append(data, '\n'), err
}

func checkExecutable(reader io.ReaderAt, arch string) error {
	file, err := elf.NewFile(reader)
	if err != nil {
		return refused("the launcher binary is not an ELF executable")
	}
	defer file.Close()
	machine := elf.EM_X86_64
	if arch == "arm64" {
		machine = elf.EM_AARCH64
	}
	if file.Class != elf.ELFCLASS64 || file.Data != elf.ELFDATA2LSB || file.Machine != machine || file.Type != elf.ET_EXEC && file.Type != elf.ET_DYN {
		return refused("the launcher executable has the wrong ELF architecture or type")
	}
	for _, program := range file.Progs {
		if program.Type == elf.PT_INTERP {
			return refused("the root launcher must be a static executable")
		}
	}
	return nil
}

// VerifyEnvelope verifies the exact signed bytes before interpreting them.
// The key and namespace are fixed; it invokes no external program.
func VerifyEnvelope(meta, signature []byte) (Envelope, error) {
	return verifyEnvelope(meta, signature, ReleasePublicKey)
}

func verifyEnvelope(meta, signature []byte, keyText string) (Envelope, error) {
	var envelope Envelope
	if len(meta) < 1 || len(meta) > RequestMax || len(signature) < 1 || len(signature) > RequestMax {
		return envelope, refused("the signed launcher envelope exceeds its bound")
	}
	block, rest := pem.Decode(signature)
	if block == nil || block.Type != sshsig.PEMType || len(bytes.TrimSpace(rest)) != 0 || len(block.Headers) != 0 {
		return envelope, refused("the launcher signature is not one SSHSIG block")
	}
	sig, err := sshsig.Unarmor(signature)
	if err != nil {
		return envelope, refused("the launcher signature cannot be decoded")
	}
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(keyText))
	if err != nil || len(bytes.TrimSpace(rest)) != 0 {
		return envelope, refused("the embedded launcher release key is invalid")
	}
	if err := sshsig.Verify(bytes.NewReader(meta), sig, pub, sig.HashAlgorithm, SignatureNamespace()); err != nil {
		return envelope, refused("the release key did not sign this launcher envelope")
	}
	// Reuse the exact-key barrier, including duplicate keys, without giving
	// envelope fields the request union's aliases or optional fields.
	keys := json.NewDecoder(bytes.NewReader(meta))
	token, err := keys.Token()
	if err != nil || token != json.Delim('{') {
		return Envelope{}, refused("the launcher envelope is not an object")
	}
	seen := map[string]bool{}
	for keys.More() {
		key, err := keys.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return Envelope{}, refused("the launcher envelope repeats or aliases a field")
		}
		seen[name] = true
		var value json.RawMessage
		if keys.Decode(&value) != nil {
			return Envelope{}, refused("the launcher envelope has an invalid value")
		}
	}
	if token, err = keys.Token(); err != nil || token != json.Delim('}') {
		return Envelope{}, refused("the launcher envelope is incomplete")
	}
	dec := json.NewDecoder(bytes.NewReader(meta))
	dec.DisallowUnknownFields()
	if dec.Decode(&envelope) != nil || dec.Decode(new(any)) != io.EOF {
		return Envelope{}, refused("the launcher release envelope has invalid fields")
	}
	var exact map[string]json.RawMessage
	if json.Unmarshal(meta, &exact) != nil || len(exact) != 4 {
		return Envelope{}, refused("the launcher release envelope does not have four exact fields")
	}
	for _, name := range []string{"role", "arch", "version", "sha256"} {
		if _, ok := exact[name]; !ok {
			return Envelope{}, refused("the launcher release envelope has an aliased field")
		}
	}
	if envelope.Role != "launch" || envelope.Arch != "amd64" && envelope.Arch != "arm64" || !releaseSemver(envelope.Version) || !supply.IsHex64(envelope.SHA256) {
		return Envelope{}, refused("the launcher release envelope has an invalid role, architecture, version or digest")
	}
	return envelope, nil
}

func newerEnvelope(envelope Envelope, current string) error {
	if envelope.Arch != runtime.GOARCH || !releaseSemver(current) || compareVersion(envelope.Version, current) <= 0 {
		return refused(fmt.Sprintf("the launcher update is not a newer %s release", runtime.GOARCH))
	}
	return nil
}
