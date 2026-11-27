//go:build !linux

package guest

func runPurgeReaderVMV1() error {
	return purgeWorkErrorV1("the fixed reader requires its original Linux guest kernel")
}

func RunPurgeReaderGitHelperV1([]string) error {
	return purgeWorkErrorV1("the fixed reader command requires its original Linux guest PID1")
}
