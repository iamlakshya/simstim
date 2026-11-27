package host

func prepareAgentProcess() error {
	return UserError("ssh_agent_refused", "this system cannot serve the SSH agent bridge",
		"Use a supported macOS or Linux host.")
}
