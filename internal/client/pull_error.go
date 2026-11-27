package client

func pullProtocol(message string) error {
	return &UserError{Msg: message, Hint: "Update the host helper, then pull again.", Code: "pull_protocol", Kind: KindFailed}
}
