package host

import (
	"fmt"
	"strconv"
)

type converterOperation uint8

const (
	convertOCIImage converterOperation = iota
	convertInstalledAgents
)

func converterArguments(operation converterOperation, inputs int, unit string) ([]string, error) {
	switch operation {
	case convertOCIImage:
		return []string{"image-guardian", strconv.Itoa(inputs), unit}, nil
	case convertInstalledAgents:
		if inputs != 0 {
			return nil, fmt.Errorf("agents guardian source descriptors belong only to its captured discovery")
		}
		return []string{"agents-guardian", unit}, nil
	default:
		return nil, fmt.Errorf("unknown closed converter operation")
	}
}
