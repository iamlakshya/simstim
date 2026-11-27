package api

// KernelBugResultV1 is an advisory check of the running host kernel. It
// changes no wall floor, host level, admission or operating system setting.
type KernelBugResultV1 struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Result   string `json:"result"`
	Severity string `json:"severity"`
	Kernel   string `json:"kernel,omitempty"`
	Package  string `json:"package,omitempty"`
	Fixed    string `json:"fixed,omitempty"`
	URL      string `json:"url"`
	Detail   string `json:"detail"`
	Hint     string `json:"hint,omitempty"`
}
