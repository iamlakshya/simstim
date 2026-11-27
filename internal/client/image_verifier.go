package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/imageverify"
	"github.com/iamlakshya/simstim/internal/supply"
)

func verifyImageSignature(ctx context.Context, bundle, payload []byte, digest, identity, issuer string, trustedRoot []byte) error {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	request := imageverify.RequestV1{Version: imageverify.Version, Role: imageverify.Role, Nonce: hex.EncodeToString(nonce[:]), Digest: digest, Identity: identity, Issuer: issuer, TrustedRoot: trustedRoot, Bundle: bundle, Payload: payload}
	if err := request.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(data) > imageverify.MaxRequest {
		return fmt.Errorf("image verification input exceeds its bounded protocol")
	}
	stage, err := prepareImageVerifier(ctx)
	if err != nil {
		return err
	}
	verifyErr := stage.verify(ctx, data)
	return errors.Join(verifyErr, stage.close())
}

type imageVerifierStage struct {
	parent, root                   *os.Root
	leaf                           string
	directory, executable, creator cachefs.Reference
	file                           *os.File
	item                           supply.Item
}

func prepareImageVerifier(ctx context.Context) (_ *imageVerifierStage, returned error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := revalidateRetainedImagePolicy(ctx); err != nil {
		return nil, err
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, imageVerifierUnavailable()
	}
	if _, ok := helpers.Manifest(); !ok {
		return nil, imageVerifierUnavailable()
	}
	manifest, err := ReleaseManifest()
	if err != nil {
		return nil, err
	}
	item, ok := manifest.Find("image-verifier", "image-verifier-"+runtime.GOOS, runtime.GOARCH)
	if !ok || item.Version == "" || item.Version != manifest.Release {
		return nil, imageVerifierUnavailable()
	}
	name := "image-verifier-" + runtime.GOOS + "-" + runtime.GOARCH
	data, ok := helpers.File(name)
	if !ok {
		return nil, imageVerifierUnavailable()
	}
	if err := supply.Check(item, name, data); err != nil {
		return nil, err
	}
	parent, err := cachefs.OpenPrivateRoot(filepath.Join(CacheDir(), "image-verifier"), true)
	if err != nil {
		return nil, err
	}
	stage := &imageVerifierStage{parent: parent, item: item}
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, stage.close())
		}
	}()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	stage.leaf = hex.EncodeToString(nonce[:])
	if err := parent.Mkdir(stage.leaf, 0o700); err != nil {
		return nil, err
	}
	stage.root, err = cachefs.OpenPrivateChild(parent, stage.leaf, false)
	if err != nil {
		return nil, err
	}
	stage.directory, err = cachefs.CaptureRoot(stage.root)
	if err != nil {
		return nil, err
	}
	writer, err := cachefs.OpenFile(stage.root, "verifier", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	stage.file = writer
	stage.creator, err = cachefs.CaptureFile(stage.root, "verifier", writer, 0o600)
	if err != nil {
		return nil, err
	}
	_, writeErr := writer.Write(data)
	syncErr := writer.Sync()
	modeErr := writer.Chmod(0o500)
	closeErr := writer.Close()
	stage.file = nil
	if err := errors.Join(writeErr, syncErr, modeErr, closeErr); err != nil {
		return nil, err
	}
	stage.file, err = cachefs.OpenFile(stage.root, "verifier", os.O_RDONLY, 0o500)
	if err != nil {
		return nil, err
	}
	stage.executable, err = cachefs.CaptureFile(stage.root, "verifier", stage.file, 0o500)
	if err != nil {
		return nil, err
	}
	if err := stage.check(ctx); err != nil {
		return nil, err
	}
	return stage, nil
}

func (s *imageVerifierStage) check(ctx context.Context) error {
	if s == nil || s.root == nil || s.parent == nil || s.file == nil {
		return imageVerifierUnavailable()
	}
	if err := s.directory.CheckRoot(s.root); err != nil {
		return err
	}
	current, err := cachefs.OpenPrivateChild(s.parent, s.leaf, false)
	if err != nil {
		return err
	}
	defer current.Close()
	if err := s.directory.CheckRoot(current); err != nil {
		return err
	}
	named, err := cachefs.OpenPrivateRoot(s.root.Name(), false)
	if err != nil {
		return err
	}
	if err := s.directory.CheckRoot(named); err != nil {
		named.Close()
		return err
	}
	if err := named.Close(); err != nil {
		return err
	}
	if err := s.executable.CheckFile(s.root, "verifier", s.file, 0o500); err != nil {
		return err
	}
	return supply.CheckOpenContext(ctx, s.item, s.file)
}

func (s *imageVerifierStage) verify(ctx context.Context, request []byte) (returned error) {
	parentContext := ctx
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command, closeExecutable, err := imageVerifierCommand(ctx, s)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, closeExecutable()) }()
	command.Env = []string{"GOMAXPROCS=1", "GOMEMLIMIT=128MiB"}
	command.Dir = s.root.Name()
	command.WaitDelay = 2 * time.Second
	command.Stdin = bytes.NewReader(request)
	output := &imageVerifierOutput{limit: imageverify.MaxResponse, cancel: cancel}
	diagnostic := &imageVerifierOutput{limit: imageverify.MaxDiagnostic, cancel: cancel}
	command.Stdout, command.Stderr = output, diagnostic
	if err := s.check(ctx); err != nil {
		return err
	}
	if err := revalidateRetainedImagePolicy(ctx); err != nil {
		return err
	}
	if err := command.Run(); err != nil {
		if parentContext.Err() != nil {
			return parentContext.Err()
		}
		if output.overflow || diagnostic.overflow {
			return fmt.Errorf("offline image verifier output exceeds its bound")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("offline image signature verification failed: %s: %w", imageVerifierDiagnostic(diagnostic.data), err)
	}
	if err := s.check(ctx); err != nil {
		return err
	}
	if err := revalidateRetainedImagePolicy(ctx); err != nil {
		return err
	}
	return imageverify.CheckResponse(output.data, s.item.Version, request)
}

func imageVerifierDiagnostic(data []byte) string {
	message := strings.TrimSpace(string(data))
	if strings.HasPrefix(message, "Error: ") {
		message = strings.TrimPrefix(message, "Error: ")
		if end := strings.LastIndex(message, "\n\nTry: "); end >= 0 {
			message = message[:end]
		}
	}
	return CleanLine(message)
}

type imageVerifierOutput struct {
	data     []byte
	limit    int
	cancel   context.CancelFunc
	overflow bool
}

func (w *imageVerifierOutput) Write(data []byte) (int, error) {
	if len(data) > w.limit-len(w.data) {
		w.overflow = true
		w.cancel()
		return 0, fmt.Errorf("offline image verifier output exceeds its bound")
	}
	w.data = append(w.data, data...)
	return len(data), nil
}

func (s *imageVerifierStage) close() error {
	if s == nil {
		return nil
	}
	var errs []error
	if s.root != nil {
		if s.file == nil && s.creator.Known() {
			file, err := cachefs.OpenFile(s.root, "verifier", os.O_RDONLY, 0o500)
			if err != nil {
				file, err = cachefs.OpenFile(s.root, "verifier", os.O_RDONLY, 0o600)
			}
			if err != nil {
				errs = append(errs, err)
			} else {
				s.file = file
			}
		}
		if s.file != nil {
			info, err := s.file.Stat()
			if err == nil {
				err = s.creator.CheckCreatedFile(s.root, "verifier", s.file, info.Mode().Perm())
			}
			if err == nil {
				errs = append(errs, s.root.Remove("verifier"))
			} else {
				errs = append(errs, err)
			}
			errs = append(errs, s.file.Close())
		}
		if s.directory.Known() {
			current, err := cachefs.OpenPrivateChild(s.parent, s.leaf, false)
			if err == nil {
				err = s.directory.CheckRoot(current)
				errs = append(errs, current.Close())
			}
			if err == nil {
				errs = append(errs, s.parent.Remove(s.leaf))
			} else {
				errs = append(errs, err)
			}
		}
		errs = append(errs, s.root.Close())
	}
	if s.parent != nil {
		errs = append(errs, s.parent.Close())
	}
	return errors.Join(errs...)
}

func imageVerifierUnavailable() error {
	return &UserError{Msg: "this client has no checked offline image verifier for this platform", Hint: "Install a release that includes its offline image verifier.", Code: "image_signature", Kind: KindRefused}
}

var _ io.Writer = (*imageVerifierOutput)(nil)
