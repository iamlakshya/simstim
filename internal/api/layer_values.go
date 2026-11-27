package api

// LayerValues is the common shape of every policy layer. A nil scalar leaves
// the broader value in place. Lists add up; a leading ! removes an item.
type LayerValues struct {
	// Sandbox holds the wall, workspace and resource settings.
	Sandbox SandboxValues `json:"sandbox,omitempty" toml:"sandbox,omitempty"`
	// Network holds network rules and the action for unknown destinations.
	Network NetworkValues `json:"network,omitempty" toml:"network,omitempty"`
	// Classifier holds the judge's settings for unknown destinations.
	Classifier ClassifierValues `json:"classifier,omitempty" toml:"classifier,omitempty"`
}

// SandboxValues holds settings shared by all six layers. Pointer fields
// distinguish an explicit false, zero or empty value from an absent key.
type SandboxValues struct {
	// Host selects local placement from a repository, profile or session layer.
	Host *HostSelectorV1 `json:"host,omitempty" toml:"host,omitempty"`
	// Wall is auto, none, process, gvisor or microvm. Auto uses the host floor.
	Wall *string `json:"wall,omitempty" toml:"wall,omitempty"`
	// Preset is locked, balanced or open.
	Preset *string `json:"preset,omitempty" toml:"preset,omitempty"`
	// Profile names the profile to apply after repository layers.
	Profile *string `json:"profile,omitempty" toml:"profile,omitempty"`
	// System is reset or keep across starts.
	System *string `json:"system,omitempty" toml:"system,omitempty"`
	// Image names a pinned image.
	Image *string `json:"image,omitempty" toml:"image,omitempty"`
	// ImagePolicy checks OCI image signatures on the laptop before use.
	ImagePolicy *ImagePolicyValues `json:"image_policy,omitempty" toml:"image_policy,omitempty"`
	// DiskMax caps the session disk.
	DiskMax *string `json:"disk_max,omitempty" toml:"disk_max,omitempty"`
	// IdleStop stops an idle session after this duration, or never.
	IdleStop *string `json:"idle_stop,omitempty" toml:"idle_stop,omitempty"`
	// Paste is media or any.
	Paste *string `json:"paste,omitempty" toml:"paste,omitempty"`
	// PullMax caps a returned bundle.
	PullMax *string `json:"pull_max,omitempty" toml:"pull_max,omitempty"`
	// CPMax caps one copy in either direction.
	CPMax *string `json:"cp_max,omitempty" toml:"cp_max,omitempty"`
	// Checkpoints is the number of checkpoints to keep.
	Checkpoints *int `json:"checkpoints,omitempty" toml:"checkpoints,omitempty"`
	// Docker starts a Docker engine in a microvm.
	Docker *bool `json:"docker,omitempty" toml:"docker,omitempty"`
	// Decoys enables decoy credentials.
	Decoys *bool `json:"decoys,omitempty" toml:"decoys,omitempty"`
	// Shell names the sandbox's interactive shell.
	Shell *string `json:"shell,omitempty" toml:"shell,omitempty"`
	// Keep lists home files to keep across starts.
	Keep []string `json:"keep,omitempty" toml:"keep,omitempty"`
	// Mounts lists host folders to grant to the sandbox.
	Mounts []string `json:"mounts,omitempty" toml:"mounts,omitempty"`
	// Secrets lists names from the laptop's secrets table.
	Secrets []string `json:"secrets,omitempty" toml:"secrets,omitempty"`
	// SSHKeys lists configured ssh-key names for the next sandbox start.
	// Changes apply at the next start; live signers keep their current selection.
	SSHKeys []string `json:"ssh_keys,omitempty" toml:"ssh_keys,omitempty"`
	// Include lists source files to seed into the workspace.
	Include []string `json:"include,omitempty" toml:"include,omitempty"`
	// Push allows pushes to this session's branches.
	Push *bool `json:"push,omitempty" toml:"push,omitempty"`
	// CPUs limits the session's processors; zero uses the agent default.
	CPUs *int `json:"cpus,omitempty" toml:"cpus,omitempty"`
	// Memory caps memory; empty uses the agent default.
	Memory *string `json:"memory,omitempty" toml:"memory,omitempty"`
	// Env contains guest variables. Narrower layers win by name.
	Env map[string]string `json:"env,omitempty" toml:"env,omitempty"`
	// CopyIn lists laptop files to copy at each start.
	CopyIn []string `json:"copy_in,omitempty" toml:"copy_in,omitempty"`
	// CopyMax caps files copied at each start.
	CopyMax *string `json:"copy_max,omitempty" toml:"copy_max,omitempty"`
	// SeedMax caps a seed from the laptop.
	SeedMax *string `json:"seed_max,omitempty" toml:"seed_max,omitempty"`
	// KeepCaches keeps package caches across starts. The built-in default is true.
	KeepCaches *bool `json:"keep_caches,omitempty" toml:"keep_caches,omitempty"`
	// Caches adds cache folders to the built-in list; !path drops one.
	Caches []string `json:"caches,omitempty" toml:"caches,omitempty"`
	// MaxRun stops a session after this duration.
	MaxRun *string `json:"max_run,omitempty" toml:"max_run,omitempty"`
	// StopGrace caps the repository stop script, at most 60s.
	StopGrace *string `json:"stop_grace,omitempty" toml:"stop_grace,omitempty"`
	// Sudo grants sudo inside a microvm.
	Sudo *bool `json:"sudo,omitempty" toml:"sudo,omitempty"`
	// PackageManagers is guarded, strict or off.
	PackageManagers *string `json:"package_managers,omitempty" toml:"package_managers,omitempty"`
}

// NetworkValues holds settings of the network door.
type NetworkValues struct {
	// Rules holds exact target/allow/method tuples and inherited removals.
	Rules []NetworkRule `json:"rules,omitempty" toml:"rules,omitempty"`

	// Allow lists allowed destinations, with an optional port or path.
	Allow []string `json:"allow,omitempty" toml:"allow,omitempty"`
	// Deny lists denied destinations, with an optional port or path.
	Deny []string `json:"deny,omitempty" toml:"deny,omitempty"`
	// Unknown is ask, deny, allow or classify. Phase one refuses unknown flows.
	Unknown *string `json:"unknown,omitempty" toml:"unknown,omitempty"`
	// Hold is how long a flow waits for an answer.
	Hold *string `json:"hold,omitempty" toml:"hold,omitempty"`
	// AskOpen is how long a held request stays open.
	AskOpen *string `json:"ask_open,omitempty" toml:"ask_open,omitempty"`
	// UploadBudget caps uploads to each host in one hour.
	UploadBudget *string `json:"upload_budget,omitempty" toml:"upload_budget,omitempty"`
	// MinReleaseAge is a minimum package release age, in whole days.
	MinReleaseAge *string `json:"min_release_age,omitempty" toml:"min_release_age,omitempty"`
	// ModelBudget caps model tokens, or zero for no cap.
	ModelBudget *int64 `json:"model_budget,omitempty" toml:"model_budget,omitempty"`
}

// ClassifierValues holds settings of an optional judge.
type ClassifierValues struct {
	// Judge is none, model or command.
	Judge *string `json:"judge,omitempty" toml:"judge,omitempty"`
	// Endpoint is the model API URL.
	Endpoint *string `json:"endpoint,omitempty" toml:"endpoint,omitempty"`
	// Model names the model at the endpoint.
	Model *string `json:"model,omitempty" toml:"model,omitempty"`
	// Key names a secret bound to the endpoint.
	Key *string `json:"key,omitempty" toml:"key,omitempty"`
	// Command names a judge on the host.
	Command *string `json:"command,omitempty" toml:"command,omitempty"`
	// Unsure is ask or deny.
	Unsure *string `json:"unsure,omitempty" toml:"unsure,omitempty"`
	// MaxScope is once or session.
	MaxScope *string `json:"max_scope,omitempty" toml:"max_scope,omitempty"`
	// Timeout is the judge's time limit.
	Timeout *string `json:"timeout,omitempty" toml:"timeout,omitempty"`
	// Rate caps judge calls in one minute.
	Rate *int `json:"rate,omitempty" toml:"rate,omitempty"`
	// RDAP enables domain-age lookups.
	RDAP *bool `json:"rdap,omitempty" toml:"rdap,omitempty"`
	// Popularity names a ranked-domain file on the host.
	Popularity *string `json:"popularity,omitempty" toml:"popularity,omitempty"`
}

// ValueOrigin identifies the layer that supplied one merged scalar or list.
type ValueOrigin struct {
	// Source distinguishes repository-file values from a same-named laptop section.
	Source LayerSource `json:"source,omitempty"`
	// Item identifies a list item or environment variable, when applicable.
	Item string `json:"item,omitempty"`

	Key   string    `json:"key"`
	Layer LayerKind `json:"layer"`
	Name  string    `json:"name,omitempty"`
}
