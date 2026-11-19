package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

// RunImageConvert is the internal descriptor-only entry point for the jail.
// It receives no host path, registry login or network authority.
func RunImageConvert(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 1 && args[0] == savedRootConverterMode {
		return runSavedRootConvert(in, out)
	}
	if len(args) != 1 {
		return fmt.Errorf("image-convert needs a layer descriptor count")
	}
	count, err := strconv.Atoi(args[0])
	if err != nil || count < 0 || count > image.MaxLayers {
		return fmt.Errorf("invalid converter layer count")
	}
	if err := imageWorkerConfinement(); err != nil {
		return err
	}
	var layers []image.Layer
	decoder := json.NewDecoder(io.LimitReader(in, image.MaxManifest))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&layers); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("converter plan has trailing content")
	}
	if len(layers) != count {
		return fmt.Errorf("converter plan and descriptor count differ")
	}
	files := make([]*os.File, count)
	for i := range files {
		files[i] = os.NewFile(uintptr(3+i), "layer")
		defer files[i].Close()
	}
	output := os.NewFile(uintptr(count+4), "output")
	defer output.Close()
	devices, err := image.Convert(context.Background(), layers, files, output)
	response := struct {
		DroppedDevices int    `json:"dropped_devices"`
		Error          string `json:"error,omitempty"`
		Code           string `json:"code,omitempty"`
	}{DroppedDevices: devices}
	if err != nil {
		response.Error = err.Error()
		response.Code = "image_convert"
		var pe *proto.Error
		if errors.As(err, &pe) {
			response.Code = pe.Code
		}
	}
	return json.NewEncoder(out).Encode(response)
}
