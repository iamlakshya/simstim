package host

import (
	"embed"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

//go:embed kernel_bugs.toml
var kernelBugFilesV1 embed.FS

type kernelBugCatalogV1 struct {
	Schema int           `toml:"schema"`
	Bugs   []kernelBugV1 `toml:"bug"`
}
type kernelBugV1 struct {
	ID          string              `toml:"id"`
	Name        string              `toml:"name"`
	What        string              `toml:"what"`
	URL         string              `toml:"url"`
	Checked     time.Time           `toml:"checked"`
	MitigatedBy []string            `toml:"mitigated_by"`
	Upstream    []kernelBugRangeV1  `toml:"upstream"`
	Distro      []kernelBugDistroV1 `toml:"distro"`
}
type kernelBugRangeV1 struct {
	From  string `toml:"from"`
	Fixed string `toml:"fixed"`
}
type kernelBugDistroV1 struct {
	ID        string `toml:"id"`
	VersionID string `toml:"version_id"`
	Flavour   string `toml:"flavour"`
	Fixed     string `toml:"fixed"`
}

var kernelBugIDV1 = regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]{4,}$`)
var kernelModuleNameV1 = regexp.MustCompile(`^[a-zA-Z0-9_]{1,80}$`)

func loadKernelBugCatalogV1() (kernelBugCatalogV1, error) {
	raw, err := kernelBugFilesV1.ReadFile("kernel_bugs.toml")
	if err != nil {
		return kernelBugCatalogV1{}, fmt.Errorf("read embedded kernel bug list: %w", err)
	}
	return decodeKernelBugCatalogV1(raw)
}

func decodeKernelBugCatalogV1(raw []byte) (kernelBugCatalogV1, error) {
	var catalog kernelBugCatalogV1
	if len(raw) > 128<<10 {
		return catalog, errors.New("kernel bug list exceeds its bound")
	}
	metadata, err := toml.Decode(string(raw), &catalog)
	if err != nil {
		return catalog, fmt.Errorf("decode kernel bug list: %w", err)
	}
	if len(metadata.Undecoded()) != 0 || catalog.Schema != 1 {
		return catalog, errors.New("kernel bug list has an unsupported format")
	}
	seen := make(map[string]bool, len(catalog.Bugs))
	for _, bug := range catalog.Bugs {
		if err := validateKernelBugEntryV1(bug, seen); err != nil {
			return catalog, err
		}
	}
	return catalog, nil
}

func validateKernelBugEntryV1(bug kernelBugV1, seen map[string]bool) error {
	if !validKernelBugHeaderV1(bug, seen) {
		return errors.New("kernel bug list has an invalid entry")
	}
	seen[bug.ID] = true
	for _, row := range bug.Upstream {
		if err := validateKernelBugUpstreamV1(row); err != nil {
			return err
		}
	}
	for _, row := range bug.Distro {
		if err := validateKernelBugDistroV1(row); err != nil {
			return err
		}
	}
	for _, condition := range bug.MitigatedBy {
		if err := validateKernelBugMitigationV1(condition); err != nil {
			return err
		}
	}
	return nil
}

func validKernelBugHeaderV1(bug kernelBugV1, seen map[string]bool) bool {
	if !kernelBugIDV1.MatchString(bug.ID) || seen[bug.ID] {
		return false
	}
	if bug.Name == "" || len(bug.Name) > 80 || bug.What == "" || bug.Checked.IsZero() {
		return false
	}
	location, err := url.Parse(bug.URL)
	return err == nil && location.Scheme == "https" && location.Host != "" && location.User == nil
}

func validateKernelBugUpstreamV1(row kernelBugRangeV1) error {
	from, err := parseCanonicalKernelVersionV1(row.From)
	if err != nil {
		return fmt.Errorf("kernel bug range has an invalid first version: %w", err)
	}
	if row.Fixed == "" {
		return nil
	}
	fixed, err := parseCanonicalKernelVersionV1(row.Fixed)
	if err != nil {
		return fmt.Errorf("kernel bug range has an invalid fixed version: %w", err)
	}
	if from.compare(fixed) >= 0 {
		return errors.New("kernel bug range has a non-increasing fixed version")
	}
	return nil
}

func validateKernelBugDistroV1(row kernelBugDistroV1) error {
	if row.ID == "" || row.VersionID == "" || strings.TrimSpace(row.ID) != row.ID || strings.TrimSpace(row.VersionID) != row.VersionID {
		return errors.New("kernel bug package scope is empty or padded")
	}
	if row.ID != "debian" && row.ID != "ubuntu" {
		return errors.New("kernel bug package scope names an unsupported distribution")
	}
	if row.Flavour != "" && !validKernelFlavourV1(row.Flavour) {
		return errors.New("kernel bug package scope has an invalid flavour")
	}
	if row.ID == "ubuntu" && row.Flavour == "" {
		return errors.New("kernel bug ubuntu package scope requires a flavour")
	}
	if row.Fixed == "" {
		return nil
	}
	fixed, err := parseDebianVersionV1(row.Fixed)
	if err != nil {
		return err
	}
	branch, err := parseCanonicalKernelVersionV1(fixed.upstream)
	if err != nil || branch.prerelease != "" {
		return errors.New("kernel bug package scope has a non-canonical upstream branch")
	}
	if row.ID == "ubuntu" {
		if _, ok := parseUbuntuPackageABIV1(fixed.revision); !ok {
			return errors.New("kernel bug ubuntu package scope has an invalid revision")
		}
	}
	return nil
}

func validateKernelBugMitigationV1(condition string) error {
	fact, value, ok := strings.Cut(condition, "=")
	if !ok || fact == "" || value == "" {
		return errors.New("kernel mitigation has no fact/value")
	}
	if module, ok := strings.CutPrefix(fact, "module:"); ok {
		if !kernelModuleNameV1.MatchString(module) || value != "absent" {
			return errors.New("kernel mitigation module is invalid")
		}
		return nil
	}
	if !knownKernelMitigationFactV1(fact) {
		return errors.New("kernel mitigation names an unknown probe fact")
	}
	return nil
}
