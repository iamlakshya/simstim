package client

import (
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/brand"
)

// loadHostsFile is the ONE canonical bounded/alias/type loader for the full
// saved-host DTO. Row consumers and the actual writer retain Default too.
func loadHostsFile() (hostsFile, error) {
	b, err := ReadConfigFile(hostsPath())
	switch {
	case errors.Is(err, os.ErrNotExist):
		return hostsFile{}, nil
	case errors.Is(err, ErrNotAFile), errors.Is(err, ErrConfigTooLarge):
		return hostsFile{}, &UserError{Msg: hostsPath() + " cannot be used: " + err.Error(), Hint: brand.Name + " config check", Code: "config_unusable"}
	case err != nil:
		return hostsFile{}, err
	}
	var f hostsFile
	if _, err := decodeConfig(string(b), &f); err != nil {
		return hostsFile{}, tomlError(hostsPath(), string(b), err)
	}
	return f, nil
}

// savedDefaultHost is the actual P112 report accessor, using that same
// canonical DTO/decoder, not another settings parser or selected-host cache.
func savedDefaultHost() (string, error) {
	file, err := loadHostsFile()
	if err != nil {
		return "", err
	}
	return file.Default, nil
}
