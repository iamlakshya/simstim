package api

import "testing"

func TestParseKernelMajorMinor(t *testing.T) {
	cases := []struct {
		in     string
		maj    int
		min    int
		wantOK bool
	}{
		{"6.12.118+deb13-cloud-amd64", 6, 12, true},
		{"5.13.0-44-generic", 5, 13, true},
		{"6.8-rc1", 6, 8, true},
		{"6.1", 6, 1, true},
		{"", 0, 0, false},
		{"linux", 0, 0, false},
		{"6.bad", 0, 0, false},
	}
	for _, c := range cases {
		maj, minVal, ok := ParseKernelMajorMinor(c.in)
		if ok != c.wantOK || maj != c.maj || minVal != c.min {
			t.Errorf("ParseKernelMajorMinor(%q) = (%d, %d, %v), want (%d, %d, %v)", c.in, maj, minVal, ok, c.maj, c.min, c.wantOK)
		}
	}
}

func TestLevelsFor(t *testing.T) {
	chvRuntime := RuntimeStatus{Items: []RuntimeItem{
		{Kind: "vmm", Name: "cloud-hypervisor", Arch: "amd64", HostKernel: ">=6.8"},
	}}
	chvAndFC := RuntimeStatus{Items: []RuntimeItem{
		{Kind: "vmm", Name: "cloud-hypervisor", Arch: "amd64", HostKernel: ">=6.8"},
		{Kind: "vmm", Name: "firecracker", Arch: "amd64", HostKernel: "5.10,6.1,6.18"},
	}}
	gvisorRuntime := RuntimeStatus{Items: []RuntimeItem{
		{Kind: "gvisor", Name: "runsc", Arch: "amd64"},
	}}

	cases := []struct {
		name      string
		probe     CapProbe
		runtime   RuntimeStatus
		wantNow   string
		wantAfter string
	}{
		{
			name: "kvm usable with matching VMM",
			probe: CapProbe{
				OS: "linux", Arch: "amd64", Kernel: "6.12.118+deb13-cloud-amd64",
				KVM:   KVM{Present: true, Usable: true},
				Bwrap: true, UserNS: true, LandlockABI: 6, Seccomp: true,
			},
			runtime:   chvRuntime,
			wantNow:   LevelMicroVM,
			wantAfter: LevelMicroVM,
		},
		{
			name: "kvm usable on kernel unsupported by runtime VMM falls back to process",
			probe: CapProbe{
				OS: "linux", Arch: "amd64", Kernel: "6.1.102",
				KVM:   KVM{Present: true, Usable: true},
				Bwrap: true, UserNS: true, LandlockABI: 3, Seccomp: true,
			},
			runtime:   chvRuntime,
			wantNow:   LevelProcess,
			wantAfter: LevelProcess,
		},
		{
			name: "kvm usable on 6.1 with firecracker runtime",
			probe: CapProbe{
				OS: "linux", Arch: "amd64", Kernel: "6.1.102",
				KVM:   KVM{Present: true, Usable: true},
				Bwrap: true, UserNS: true, LandlockABI: 3, Seccomp: true,
			},
			runtime:   chvAndFC,
			wantNow:   LevelMicroVM,
			wantAfter: LevelMicroVM,
		},
		{
			name: "kvm present not open yet upgrades after root step",
			probe: CapProbe{
				OS: "linux", Arch: "amd64", Kernel: "6.12.118+deb13-cloud-amd64",
				KVM:   KVM{Present: true, Usable: false},
				Bwrap: true, UserNS: true, LandlockABI: 6, Seccomp: true,
			},
			runtime:   chvRuntime,
			wantNow:   LevelProcess,
			wantAfter: LevelMicroVM,
		},
		{
			name: "debian 13 without bwrap yet and no VMM runtime",
			probe: CapProbe{
				OS: "linux", Arch: "amd64", Kernel: "6.12.118+deb13-cloud-amd64",
				KVM:   KVM{Present: true, Usable: false},
				Bwrap: false, UserNS: true, LandlockABI: 6, Seccomp: true,
				Walls: []WallProbe{{Driver: "process", Level: LevelProcess, OK: false, Reason: "bubblewrap (bwrap) is not installed"}},
			},
			runtime:   RuntimeStatus{},
			wantNow:   LevelNone,
			wantAfter: LevelProcess,
		},
		{
			name: "ubuntu 24.04 restricted by apparmor unlocks process after profile",
			probe: CapProbe{
				OS: "linux", Arch: "amd64", Kernel: "6.8.0-45-generic",
				Bwrap: true, UserNS: false, UserNSRestricted: true, LandlockABI: 4, Seccomp: true,
				Walls: []WallProbe{{Driver: "process", Level: LevelProcess, OK: false, Reason: "Ubuntu's AppArmor user-namespace restriction blocks bubblewrap"}},
			},
			runtime:   RuntimeStatus{},
			wantNow:   LevelNone,
			wantAfter: LevelProcess,
		},
		{
			name: "gvisor runtime with userns",
			probe: CapProbe{
				OS: "linux", Arch: "amd64", Kernel: "6.12.0",
				Bwrap: true, UserNS: true, LandlockABI: 6, Seccomp: true,
			},
			runtime:   gvisorRuntime,
			wantNow:   LevelGVisor,
			wantAfter: LevelGVisor,
		},
		{
			name: "gvisor runtime with ubuntu apparmor restriction",
			probe: CapProbe{
				OS: "linux", Arch: "amd64", Kernel: "6.8.0",
				UserNS: false, UserNSRestricted: true, LandlockABI: 4, Seccomp: true,
			},
			runtime:   gvisorRuntime,
			wantNow:   LevelNone,
			wantAfter: LevelGVisor,
		},
		{
			name: "nitro present not usable yet",
			probe: CapProbe{
				OS: "linux", Arch: "amd64", Kernel: "6.18.51-amzn2023",
				Nitro: &Nitro{Present: true, Usable: false},
				Bwrap: true, UserNS: true, LandlockABI: 6, Seccomp: true,
			},
			runtime:   chvRuntime,
			wantNow:   LevelProcess,
			wantAfter: LevelMicroVM,
		},
		{
			name: "docker container with no userns and no apparmor restriction",
			probe: CapProbe{
				OS: "linux", Arch: "amd64", Kernel: "6.12.0",
				Bwrap: true, UserNS: false, UserNSRestricted: false, LandlockABI: 6, Seccomp: true,
			},
			runtime:   RuntimeStatus{},
			wantNow:   LevelNone,
			wantAfter: LevelNone,
		},
		{
			name: "no landlock",
			probe: CapProbe{
				OS: "linux", Arch: "amd64", Kernel: "6.1.0",
				Bwrap: true, UserNS: true, LandlockABI: 0, Seccomp: true,
			},
			runtime:   RuntimeStatus{},
			wantNow:   LevelNone,
			wantAfter: LevelNone,
		},
		{
			name: "no seccomp",
			probe: CapProbe{
				OS: "linux", Arch: "amd64", Kernel: "6.12.0",
				Bwrap: true, UserNS: true, LandlockABI: 6, Seccomp: false,
			},
			runtime:   RuntimeStatus{},
			wantNow:   LevelNone,
			wantAfter: LevelNone,
		},
		{
			name: "non-linux host",
			probe: CapProbe{
				OS: "darwin", Arch: "arm64",
			},
			runtime:   RuntimeStatus{},
			wantNow:   LevelNone,
			wantAfter: LevelNone,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			now, after := LevelsFor(c.probe, c.runtime)
			if now != c.wantNow || after != c.wantAfter {
				t.Errorf("LevelsFor = (%q, %q), want (%q, %q)", now, after, c.wantNow, c.wantAfter)
			}
		})
	}
}

func TestKVMStaleGroup(t *testing.T) {
	cases := []struct {
		name string
		kvm  KVM
		want bool
	}{
		{
			name: "user in kvm group in /etc/group but missing in current process",
			kvm:  KVM{Present: true, Usable: false, InGroup: true, InGroupNow: false, InGroupManager: true},
			want: true,
		},
		{
			name: "kvm already usable via ACL despite missing process group",
			kvm:  KVM{Present: true, Usable: true, ACLUser: true, InGroup: true, InGroupNow: false},
			want: false,
		},
		{
			name: "user already has kvm group in current process",
			kvm:  KVM{Present: true, Usable: false, InGroup: true, InGroupNow: true},
			want: false,
		},
		{
			name: "user not in kvm group yet",
			kvm:  KVM{Present: true, Usable: false, InGroup: false, InGroupNow: false},
			want: false,
		},
		{
			name: "no /dev/kvm present",
			kvm:  KVM{Present: false, Usable: false, InGroup: true, InGroupNow: false},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.kvm.StaleGroup(); got != c.want {
				t.Errorf("StaleGroup(%+v) = %v, want %v", c.kvm, got, c.want)
			}
		})
	}
}
