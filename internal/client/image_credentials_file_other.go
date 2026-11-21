//go:build !unix

package client

func openImageCredentialFile(path string) (configFile, error) {
	return openConfig(path)
}
