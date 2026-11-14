package host

func agentSocketError() error {
	return UserError("agent_socket", "the session cannot use a private SSH agent socket",
		"Check the host's private runtime folder and attach again from a computer with a running SSH agent.")
}
