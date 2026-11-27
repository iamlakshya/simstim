//go:build !linux

package supervisor

func noneSelfBirthV1() (string, error) {
	return "", execRefusal("the original None cleanup record requires its Linux host kernel")
}
