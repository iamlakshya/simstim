package host

import "testing"

func TestKernelDebianVersionOrderIncludesEpochTildeAndNumericRuns(t *testing.T) {
	for _, row := range []struct {
		a, b string
		want int
	}{
		{"6.12.100-1", "6.12.99-9", 1},
		{"6.12.100-1~bpo13+1", "6.12.100-1", -1},
		{"1:6.8.0-1", "6.12.100-1", 1},
		{"0001:6.8.0-01", "1:6.8.0-1", 0},
		{"6.8.0-147.147", "6.8.0-149.149", -1},
		{"6.8.0-149.149~24.04.1", "6.8.0-149.149", -1},
		{"6.8.0-1ubuntu1", "6.8.0-1", 1},
		{"6.8.0~rc1-1", "6.8.0-1", -1},
		{"6.8.0", "6.8.0-0", 0},
		{"6.8.0+vendor", "6.8.0", 1},
	} {
		got, err := compareDebianVersionsV1(row.a, row.b)
		if err != nil || got != row.want {
			t.Fatal(row, got, err)
		}
		reverse, err := compareDebianVersionsV1(row.b, row.a)
		if err != nil || reverse != -row.want {
			t.Fatal("reverse order", row, reverse, err)
		}
	}
	for _, bad := range []string{"", "x", "1:", "a:6.8.0", "6.8.0-", "6.8.0\n"} {
		if _, err := compareDebianVersionsV1(bad, "6.8.0-1"); err == nil {
			t.Fatal("malformed package version accepted", bad)
		}
	}
}

func TestKernelVersionParsingUsesNumericReleaseNotLexicalOrder(t *testing.T) {
	ordered := []string{
		"6.8.0",
		"6.12.96",
		"6.12.104-pre0",
		"6.12.104-pre1",
		"6.12.104-pre2",
		"6.12.104-pre10",
		"6.12.104-rc1",
		"6.12.104-rc4",
		"6.12.104-rc10",
		"6.12.104",
		"6.12.105-cloud-amd64",
		"7.2.0-pre0",
		"7.2.0-rc4",
		"7.2.0",
	}
	parsed := make([]kernelVersionV1, len(ordered))
	for i, text := range ordered {
		v, err := parseKernelVersionV1(text)
		if err != nil {
			t.Fatal(text, err)
		}
		parsed[i] = v
	}
	for i := range parsed {
		if parsed[i].compare(parsed[i]) != 0 {
			t.Fatal("self compare failed", ordered[i])
		}
		for j := i + 1; j < len(parsed); j++ {
			if parsed[i].compare(parsed[j]) >= 0 || parsed[j].compare(parsed[i]) <= 0 {
				t.Fatal("order mismatch", ordered[i], ordered[j])
			}
		}
	}

	for _, equal := range [][2]string{
		{"7.2", "7.2.0"},
		{"7.2-rc4", "7.2.0-rc4"},
		{"7.2.0-rc4-amd64", "7.2.0-rc4"},
		{"7.2.0-rc4+git123", "7.2.0-rc4"},
		{"7.2.0-rc4.1", "7.2.0-rc4"},
	} {
		left, err := parseKernelVersionV1(equal[0])
		if err != nil {
			t.Fatal(equal[0], err)
		}
		right, err := parseKernelVersionV1(equal[1])
		if err != nil || left.compare(right) != 0 {
			t.Fatal(equal, left, right, err)
		}
	}

	final72, err := parseKernelVersionV1("7.2.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, vendorFinal := range []string{
		"7.2.0-custom-rc4",
		"7.2.0-1-generic",
		"7.2.0-cloud-amd64",
		"7.2.0-rc",
		"7.2.0-pre",
		"7.2.0-rc-4",
		"7.2.0-preempt1",
		"7.2.0-rc4foo",
		"7.2.0-RC4",
		"7.2.0.1-rc4",
	} {
		got, err := parseKernelVersionV1(vendorFinal)
		if err != nil || got.compare(final72) != 0 {
			t.Fatal("vendor suffix became prerelease", vendorFinal, got, err)
		}
	}

	for _, bad := range []string{
		"",
		"unreadable",
		"7",
		"7.",
		"7.2.",
		"7.2.x",
		"99999999999999999999999.0.0",
		"7.2.0-rc99999999999999999999999",
	} {
		if _, err := parseKernelVersionV1(bad); err == nil {
			t.Fatal("invalid runtime version accepted", bad)
		}
	}
}

func TestCanonicalKernelVersionParsingRejectsTyposAndVendorSuffixes(t *testing.T) {
	for _, valid := range []string{"4.11.0", "6.13.0-pre0", "6.13.0-pre1", "7.1.0-rc5", "7.2.0"} {
		canonical, err := parseCanonicalKernelVersionV1(valid)
		if err != nil {
			t.Fatal(valid, err)
		}
		runtimeParsed, err := parseKernelVersionV1(valid)
		if err != nil || canonical.compare(runtimeParsed) != 0 {
			t.Fatal(valid, canonical, runtimeParsed, err)
		}
	}
	for _, invalid := range []string{
		"",
		" 7.2.0",
		"7.2.0 ",
		"7.2",
		"7.1-rc5",
		"07.2.0",
		"7.02.0",
		"7.2.00",
		"7.2.0-pre00",
		"7.2.0-rc0",
		"7.2.0-rc04",
		"7.2.0-rc",
		"7.2.0-pre",
		"7.2.0-rc4foo",
		"7.2.0-custom-rc4",
		"7.2.0-rc4-amd64",
		"7.2.0+git1",
		"7.2.0-1-generic",
		"7.2.0.1",
		"7.2.0-RC4",
		"99999999999999999999999.0.0",
		"7.2.0-rc99999999999999999999999",
	} {
		if _, err := parseCanonicalKernelVersionV1(invalid); err == nil {
			t.Fatal("non-canonical catalogue version accepted", invalid)
		}
	}
}
