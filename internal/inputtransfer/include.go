package inputtransfer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strconv"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

// IncludeName is the only staged include filename. Its ordinal comes from the
// complete canonical declaration, never a helper-supplied pathname.
func IncludeName(index int) string { return "include-" + strconv.Itoa(index) }

func receiveInclude(ctx context.Context, reader *bufio.Reader, options ReceiveOptions, index int, file api.InputFile) error {
	if err := options.BeforeCreate(ctx, options.Stage); err != nil {
		return err
	}
	name := IncludeName(index)
	out, err := options.Stage.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = receiveItem(ctx, reader, item{Kind: "include", Index: index, Size: file.Size, SHA256: file.SHA256}, out)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil || closeErr != nil {
		_ = options.Stage.Remove(name)
		if err == nil {
			err = closeErr
		}
	}
	return err
}

// ApplyIncludes revalidates the whole staged declaration and every loose
// include hash, then uses the same atomic rooted destination writer. The fixed
// helper caller MUST be UID1000 and run only after joined native clones. The
// same owner joins this helper before Complete, Store, scripts or agents.
// Include metadata does not grant local read permission or bypass H5's review.
func ApplyIncludes(ctx context.Context, options ReceiveOptions, summary api.InputSummary) (err error) {
	if options.Stage == nil || options.Root == nil || options.BeforeCreate == nil {
		return protocol("reviewed include application has no owned roots or capacity guard")
	}
	if err := summary.Validate(options.CopyMax, options.SeedMax); err != nil {
		return err
	}
	if summary.Nonce != options.Nonce || summary.Digest != options.Digest {
		return protocol("reviewed include application changed input scope")
	}
	if err := ValidateProtected(options.Protected); err != nil {
		return err
	}
	spool, err := files.OpenRegular(options.Stage, "declarations")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, spool.Close()) }()
	var end bytes.Buffer
	if err = sendEvent(ctx, proto.NewWriter(&end), options.Scope, "input.declarations_end", summary.FileCount, nil); err != nil {
		return err
	}
	// All staged declarations are checked before any include destination.
	// The second pass compares each immutable per-row hash again.
	hashes, err := receiveDeclarations(ctx, bufio.NewReader(io.MultiReader(spool, &end)), io.Discard, options, summary, nil)
	if err != nil {
		return err
	}
	return eachDeclaration(ctx, spool, options.Scope, hashes, func(index int, file api.InputFile) (err error) {
		if !file.Include {
			return nil
		}
		input, err := files.OpenRegular(options.Stage, IncludeName(index))
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, input.Close()) }()
		info, err := input.Stat()
		if err != nil || info.Size() != file.Size {
			return protocol("staged include size changed")
		}
		return applyFile(ctx, options, file, func(out io.Writer) error {
			sum := sha256.New()
			n, err := io.Copy(io.MultiWriter(out, sum), &contextReader{ctx: ctx, reader: io.LimitReader(input, file.Size)})
			if err != nil {
				return err
			}
			var extra [1]byte
			more, endErr := input.Read(extra[:])
			if n != file.Size || more != 0 || endErr != io.EOF || hex.EncodeToString(sum.Sum(nil)) != file.SHA256 {
				return protocol("staged include bytes changed")
			}
			return nil
		}, nil)
	})
}
