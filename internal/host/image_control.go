package host

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/jail"
)

const imageControlLimit = 2 * image.MaxManifest

type imageGuardianFrame struct {
	Phase        string `json:"phase"`
	InvocationID string `json:"invocation_id,omitempty"`
	ControlGroup string `json:"control_group,omitempty"`
	Output       []byte `json:"output,omitempty"`
	Failure      string `json:"failure,omitempty"`
}

func imageAcknowledge(out io.Writer) error {
	_, err := out.Write([]byte{0})
	if err != nil {
		return fmt.Errorf("acknowledge image guardian: %w", err)
	}
	return nil
}
func imageWaitAck(in io.Reader) error {
	var ack [1]byte
	if _, err := io.ReadFull(in, ack[:]); err != nil {
		return fmt.Errorf("wait for image guardian acknowledgement: %w", err)
	}
	if ack[0] != 0 {
		return fmt.Errorf("invalid image guardian acknowledgement")
	}
	return nil
}

func sendImagePlan(out io.Writer, layers []image.Layer) error {
	data, err := json.Marshal(layers)
	if err != nil {
		return fmt.Errorf("encode image conversion plan: %w", err)
	}
	return sendConverterPlan(out, data)
}
func sendConverterPlan(out io.Writer, data []byte) error {
	if int64(len(data)) > image.MaxManifest {
		return fmt.Errorf("image conversion plan exceeds its bound")
	}
	if err := imageAcknowledge(out); err != nil {
		return err
	}
	if err := binary.Write(out, binary.LittleEndian, uint32(len(data))); err != nil {
		return fmt.Errorf("write image plan length: %w", err)
	}
	if _, err := out.Write(data); err != nil {
		return fmt.Errorf("write image plan: %w", err)
	}
	return nil
}

func receiveImagePlan(in io.Reader) ([]byte, error) {
	if err := imageWaitAck(in); err != nil {
		return nil, err
	}
	return receiveAcknowledgedImagePlanV1(in)
}

func receiveAcknowledgedImagePlanV1(in io.Reader) ([]byte, error) {
	var size uint32
	if err := binary.Read(in, binary.LittleEndian, &size); err != nil {
		return nil, fmt.Errorf("read image plan length: %w", err)
	}
	if size == 0 || int64(size) > image.MaxManifest {
		return nil, fmt.Errorf("image conversion plan exceeds its bound")
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(in, data); err != nil {
		return nil, fmt.Errorf("read image plan: %w", err)
	}
	return data, nil
}

func runImageGuardianJail(ctx context.Context, spec jail.Spec, scope imageScopeInfo) error {
	encoder := json.NewEncoder(os.Stdout)
	if err := encoder.Encode(imageGuardianFrame{Phase: "ready", InvocationID: scope.InvocationID, ControlGroup: scope.ControlGroup}); err != nil {
		return fmt.Errorf("report image guardian readiness: %w", err)
	}
	plan, err := receiveImagePlan(os.Stdin)
	if err != nil {
		return err
	}
	data, err := runConverterJail(ctx, spec, func(output io.Writer) error { _, err := output.Write(plan); return err })
	return reportImageDone(encoder, data, err)
}

func reportImageDone(encoder *json.Encoder, data []byte, failure error) error {
	return reportConverterDone(encoder, os.Stdin, data, failure)
}

func reportConverterDone(encoder *json.Encoder, control io.Reader, data []byte, failure error) error {
	frame := imageGuardianFrame{Phase: "done", Output: data}
	if failure != nil {
		frame.Failure = failure.Error()
	}
	if err := encoder.Encode(frame); err != nil {
		return fmt.Errorf("report jailed image outcome: %w", err)
	}
	return imageWaitAck(control)
}
