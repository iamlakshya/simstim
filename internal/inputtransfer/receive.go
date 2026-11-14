package inputtransfer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

// ReceiveOptions contains pinned guest roots and protected supervisor facts.
// Stage is a newly created private nonce directory owned by the actual Init
// process. Root is the guest filesystem, never the host's trusted storage.
type ReceiveOptions struct {
	Scope
	Stage     *os.Root
	Root      *os.Root
	CopyMax   int64
	SeedMax   int64
	Protected []string
	// Expected is the authenticated New summary on a trusted supervisor.
	// A fixed guest helper may omit it and checks the registered scope digest.
	Expected *api.InputSummary
	// Declared audits only validated paths before their bytes are exposed.
	// It may not promote a guest receipt to source or secret authority.
	Declared func(api.InputFile) error
	// BeforeCreate is the actual helper's current filesystem/error guard.
	// Its owner supplies it for every explicit and implicit directory and
	// temporary file, while its cancellation also observes the disk watch.
	BeforeCreate func(context.Context, *os.Root) error
}

// Staged carries no authoritative pathname or new seed identity. The actual
// owner uses its already protected repository list and pinned Stage directory.
type Staged struct {
	Files        int
	Repositories int
}

// Receive validates all declarations before destination writes. It copies
// ordinary files atomically one at a time and stages bundle bytes without
// executing Git. Init must join this helper, clone through its SAME owned Git
// runner, then emit Complete and close the stream before agent startup.
func Receive(ctx context.Context, input io.Reader, output io.Writer, options ReceiveOptions) (_ Staged, err error) {
	if err := options.Scope.validate(); err != nil {
		return Staged{}, err
	}
	if input == nil || output == nil || options.Stage == nil || options.Root == nil || options.CopyMax <= 0 || options.SeedMax <= 0 || options.BeforeCreate == nil {
		return Staged{}, protocol("input receiver has no trusted roots or positive limits")
	}
	if err = ValidateProtected(options.Protected); err != nil {
		return Staged{}, err
	}
	reader, writer := bufio.NewReader(input), proto.NewWriter(output)
	summary, err := receiveSummary(ctx, reader)
	if err != nil {
		return Staged{}, err
	}
	if summary.Nonce != options.Nonce || summary.Digest != options.Digest {
		return Staged{}, protocol("input summary has a different scope")
	}
	if err = summary.Validate(options.CopyMax, options.SeedMax); err != nil {
		return Staged{}, err
	}
	if options.Expected != nil && !summary.Equal(*options.Expected) {
		return Staged{}, protocol("input summary differs from the authenticated request")
	}
	if err = options.BeforeCreate(ctx, options.Stage); err != nil {
		return Staged{}, err
	}
	spool, err := options.Stage.OpenFile("declarations", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return Staged{}, err
	}
	spoolClosed := false
	defer func() {
		if !spoolClosed {
			err = errors.Join(err, spool.Close())
		}
	}()
	declarationHashes, err := receiveDeclarations(ctx, reader, spool, options, summary, nil)
	if err != nil {
		return Staged{}, err
	}
	if err = spool.Sync(); err != nil {
		return Staged{}, err
	}
	if _, err = spool.Seek(0, io.SeekStart); err != nil {
		return Staged{}, err
	}
	if err = sendEvent(ctx, writer, options.Scope, "input.ready", 0, nil); err != nil {
		return Staged{}, err
	}
	declarations := bufio.NewReader(spool)
	for index := 0; index < summary.FileCount; index++ {
		message, err := expectEvent(ctx, declarations, options.Scope, "input.file", index)
		if err != nil {
			return Staged{}, err
		}
		data, err := json.Marshal(*message.File)
		if err != nil || sha256.Sum256(data) != declarationHashes[index] {
			return Staged{}, protocol("the inert declaration spool changed before copying")
		}
		if options.Declared != nil {
			if err = options.Declared(*message.File); err != nil {
				return Staged{}, err
			}
		}
		if message.File.Include {
			if err = receiveInclude(ctx, reader, options, index, *message.File); err != nil {
				return Staged{}, err
			}
			continue
		}
		if err = receiveFile(ctx, reader, options, index, *message.File); err != nil {
			return Staged{}, err
		}
	}
	for index, repository := range summary.Repositories {
		name := BundleName(index)
		if err = options.BeforeCreate(ctx, options.Stage); err != nil {
			return Staged{}, err
		}
		f, err := options.Stage.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return Staged{}, err
		}
		declaration := item{Kind: "repository", Index: index, Size: repository.Size, SHA256: repository.SHA256}
		err = receiveItem(ctx, reader, declaration, f)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			_ = options.Stage.Remove(name)
			return Staged{}, err
		}
		if closeErr != nil {
			return Staged{}, closeErr
		}
	}
	count := summary.FileCount + len(summary.Repositories)
	if _, err = expectEvent(ctx, reader, options.Scope, "input.data_end", count); err != nil {
		return Staged{}, err
	}
	if _, err = readFrame(ctx, reader); !isEOF(err) {
		return Staged{}, protocol("input data ended without exact EOF")
	}
	spoolClosed = true
	if err = spool.Close(); err != nil {
		return Staged{}, err
	}
	if err = sendEvent(ctx, writer, options.Scope, "input.staged", count, nil); err != nil {
		return Staged{}, err
	}
	return Staged{Files: summary.FileCount, Repositories: len(summary.Repositories)}, nil
}

// BundleName derives the only bundle filename that a native clone adapter may
// open through its owned Stage. A helper receipt cannot substitute a path.
func BundleName(index int) string { return "bundle-" + strconv.Itoa(index) }

func receiveSummary(ctx context.Context, reader *bufio.Reader) (api.InputSummary, error) {
	frame, err := readFrame(ctx, reader)
	if err != nil {
		return api.InputSummary{}, err
	}
	var begin item
	if frame.Type != proto.TUploadBegin || decodeItem(frame.Payload, &begin) != nil || begin.Kind != "summary" || begin.Index != 0 || begin.Size <= 0 || begin.Size > proto.MaxFrame-1 {
		return api.InputSummary{}, protocol("invalid input summary transport")
	}
	var data bytes.Buffer
	if err = receiveItemBody(ctx, reader, begin, &data); err != nil {
		return api.InputSummary{}, err
	}
	var summary api.InputSummary
	if err = decodeSummary(data.Bytes(), &summary); err != nil {
		return summary, err
	}
	return summary, nil
}

func receiveDeclarations(ctx context.Context, reader *bufio.Reader, spool io.Writer, options ReceiveOptions, summary api.InputSummary, progress func(int64)) ([][32]byte, error) {
	digest, err := api.NewInputDigest(summary)
	if err != nil {
		return nil, err
	}
	hashes := make([][32]byte, 0, summary.FileCount)
	writer := proto.NewWriter(spool)
	seen := make(map[string]bool, summary.FileCount)
	var previous string
	var total int64
	for index := 0; index < summary.FileCount; index++ {
		message, err := expectEvent(ctx, reader, options.Scope, "input.file", index)
		if err != nil {
			return nil, err
		}
		file := *message.File
		plan := api.SessionInputs{Version: api.SessionInputsV1, Nonce: summary.Nonce, Files: []api.InputFile{file}, Repositories: summary.Repositories, Identity: summary.Identity}
		if err = plan.Validate(options.CopyMax, options.SeedMax); err != nil {
			return nil, err
		}
		if index > 0 && file.Path <= previous || protectedTarget(file.Path, options.Protected) || file.Size > summary.CopyBytes-total {
			return nil, protocol("input declaration is overlapping, protected, unsorted or excessive")
		}
		for parent := path.Dir(file.Path); parent != "/"; parent = path.Dir(parent) {
			if seen[parent] {
				return nil, protocol("an input file is another declaration's parent")
			}
		}
		seen[file.Path], previous = true, file.Path
		total += file.Size
		if err = api.AddInputFileDigest(digest, file); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(file)
		if err != nil {
			return nil, err
		}
		hashes = append(hashes, sha256.Sum256(encoded))
		if err = writeJSON(ctx, writer, proto.TEvent, message); err != nil {
			return nil, err
		}
		if progress != nil {
			// Account only the complete canonical row actually written to
			// the owned spool, never raw whitespace/header/read activity.
			row, err := json.Marshal(message)
			if err != nil {
				return nil, err
			}
			progress(int64(len(row) + 5))
		}
	}
	if _, err = expectEvent(ctx, reader, options.Scope, "input.declarations_end", summary.FileCount); err != nil {
		return nil, err
	}
	if total != summary.CopyBytes || hex.EncodeToString(digest.Sum(nil)) != summary.Digest {
		return nil, protocol("the received input set does not match its complete sealed manifest")
	}
	return hashes, nil
}

func protectedTarget(name string, protected []string) bool {
	if !api.InputGuestPath(name) {
		return true
	}
	for _, root := range protected {
		if name == root || strings.HasPrefix(name, root+"/") || strings.HasPrefix(root, name+"/") {
			return true
		}
	}
	return false
}

// VerifyBundle rechecks the exact protected bundle bytes through a no-follow
// descriptor before the Init owner's fixed native Git recipe may use them.
func VerifyBundle(ctx context.Context, stage *os.Root, index int, repository api.InputRepository) error {
	if stage == nil || index < 0 || repository.Size <= 0 {
		return protocol("invalid staged repository ordinal or size")
	}
	file, err := files.OpenRegular(stage, BundleName(index))
	if err != nil {
		return err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil || st.Size() != repository.Size {
		return protocol("staged bundle size changed")
	}
	sum := sha256.New()
	n, err := io.Copy(sum, &contextReader{ctx: ctx, reader: io.LimitReader(file, repository.Size)})
	if err != nil {
		return err
	}
	var extra [1]byte
	more, endErr := file.Read(extra[:])
	if n != repository.Size || more != 0 || endErr != io.EOF || hex.EncodeToString(sum.Sum(nil)) != repository.SHA256 {
		return protocol("staged bundle bytes changed")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

// EncodeControl encodes one trusted prefix for the fixed helper's stdin.
// It is sent by Init before forwarding any client-controlled domain frame.
func EncodeControl(control Control) ([]byte, error) {
	if err := control.Scope.validate(); err != nil {
		return nil, err
	}
	if control.CopyMax <= 0 || control.SeedMax <= 0 {
		return nil, protocol("input control requires positive trusted limits")
	}
	if err := ValidateProtected(control.Protected); err != nil {
		return nil, err
	}
	if control.Protected == nil {
		control.Protected = []string{}
	}
	var out bytes.Buffer
	if err := writeJSON(context.Background(), proto.NewWriter(&out), proto.TEvent, control); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// DecodeControl reads the trusted Init prefix through the same bounded parser.
func DecodeControl(input *bufio.Reader) (Control, error) {
	frame, err := proto.ReadLimit(input, FrameMax)
	if err != nil {
		return Control{}, err
	}
	var control Control
	allowed := []string{"session", "nonce", "digest", "copy_max", "seed_max", "protected"}
	if frame.Type != proto.TEvent {
		return control, protocol("missing trusted input control prefix")
	}
	if _, err = decodeObject(frame.Payload, &control, allowed, allowed); err != nil {
		return control, err
	}
	if err = control.Scope.validate(); err != nil || control.CopyMax <= 0 || control.SeedMax <= 0 {
		return control, protocol("invalid trusted input control prefix")
	}
	if err = ValidateProtected(control.Protected); err != nil {
		return control, err
	}
	return control, nil
}
