package gvisor

// OCI runtime specification structures (subset of OCI 1.0.2; no Hooks field so
// no OCI hooks can ever be emitted).
type ociSpec struct {
	OCIVersion string     `json:"ociVersion"`
	Process    ociProcess `json:"process"`
	Root       ociRoot    `json:"root"`
	Hostname   string     `json:"hostname"`
	Mounts     []ociMount `json:"mounts"`
	Linux      ociLinux   `json:"linux"`
}

type ociProcess struct {
	Terminal        bool             `json:"terminal"`
	User            ociUser          `json:"user"`
	Args            []string         `json:"args"`
	Env             []string         `json:"env"`
	Cwd             string           `json:"cwd"`
	Capabilities    *ociCapabilities `json:"capabilities"`
	NoNewPrivileges bool             `json:"noNewPrivileges"`
}

type ociUser struct {
	UID uint32 `json:"uid"`
	GID uint32 `json:"gid"`
}

type ociCapabilities struct {
	Bounding    []string `json:"bounding"`
	Effective   []string `json:"effective"`
	Inheritable []string `json:"inheritable"`
	Permitted   []string `json:"permitted"`
	Ambient     []string `json:"ambient"`
}

type ociRoot struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"readonly"`
}

type ociMount struct {
	Destination string   `json:"destination"`
	Type        string   `json:"type"`
	Source      string   `json:"source"`
	Options     []string `json:"options,omitempty"`
}

type ociLinux struct {
	UIDMappings   []ociIDMapping `json:"uidMappings"`
	GIDMappings   []ociIDMapping `json:"gidMappings"`
	Namespaces    []ociNamespace `json:"namespaces"`
	Seccomp       *ociSeccomp    `json:"seccomp,omitempty"`
	MaskedPaths   []string       `json:"maskedPaths,omitempty"`
	ReadonlyPaths []string       `json:"readonlyPaths,omitempty"`
}

type ociIDMapping struct {
	ContainerID uint32 `json:"containerID"`
	HostID      uint32 `json:"hostID"`
	Size        uint32 `json:"size"`
}

type ociNamespace struct {
	Type string `json:"type"`
}

type ociSeccomp struct {
	DefaultAction string           `json:"defaultAction"`
	Syscalls      []ociSyscallRule `json:"syscalls"`
}

type ociSyscallRule struct {
	Names    []string        `json:"names"`
	Action   string          `json:"action"`
	ErrnoRet *uint           `json:"errnoRet,omitempty"`
	Args     []ociSeccompArg `json:"args,omitempty"`
}

type ociSeccompArg struct {
	Index uint   `json:"index"`
	Value uint64 `json:"value"`
	Op    string `json:"op"`
}

func buildOCISeccomp() *ociSeccomp {
	eperm := uint(1)
	enosys := uint(38)
	return &ociSeccomp{
		DefaultAction: "SCMP_ACT_ALLOW",
		Syscalls: []ociSyscallRule{
			{
				Names:    []string{"unshare", "mount", "umount2", "bpf", "io_uring_setup", "io_uring_enter", "io_uring_register"},
				Action:   "SCMP_ACT_ERRNO",
				ErrnoRet: &eperm,
			},
			{
				Names:    []string{"clone3"},
				Action:   "SCMP_ACT_ERRNO",
				ErrnoRet: &enosys,
			},
			{
				// Block AF_VSOCK (40) inside Sentry as defence in depth.
				Names:    []string{"socket"},
				Action:   "SCMP_ACT_ERRNO",
				ErrnoRet: &eperm,
				Args:     []ociSeccompArg{{Index: 0, Value: 40, Op: "SCMP_CMP_EQ"}},
			},
			{
				// Block NETLINK_AUDIT (proto 9 on AF_NETLINK = 16).
				Names:    []string{"socket"},
				Action:   "SCMP_ACT_ERRNO",
				ErrnoRet: &eperm,
				Args: []ociSeccompArg{
					{Index: 0, Value: 16, Op: "SCMP_CMP_EQ"},
					{Index: 2, Value: 9, Op: "SCMP_CMP_EQ"},
				},
			},
		},
	}
}
