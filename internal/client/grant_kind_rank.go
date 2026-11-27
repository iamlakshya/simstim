package client

func grantKindRank(k OutboundKind) int {
	switch k {
	case OutboundSecret:
		return 1
	case OutboundSSHAgent:
		return 2
	case OutboundSSHKey:
		return 3
	case OutboundAlertToken:
		return 4
	case OutboundRepo:
		return 5
	case OutboundPaste:
		return 6
	case OutboundPush:
		return 7
	case OutboundPublicPush:
		return 8
	case OutboundRootRun:
		return 9
	default:
		return 99
	}
}
