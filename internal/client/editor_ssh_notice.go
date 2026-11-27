package client

import (
	"bytes"
	"context"
	"io"

	"github.com/iamlakshya/simstim/internal/brand"
)

const EditorSSHTrustNotice = "Remote editor extensions can run commands on this laptop through their remote features. For a repository you do not trust, use plain SSH or SFTP, or the editor's restricted mode."

// ShowEditorSSHTrustNotice records only a successfully displayed local notice.
// This UI state grants no host, channel, file or credential permission.
func ShowEditorSSHTrustNotice(ctx context.Context, display func(string) error) error {
	if display == nil {
		return editorSSHNoticeErrorV1()
	}
	return withStateFileFD(ctx, nil, "ssh-editor-notice-v1", true, func(view stateFileView) (stateFileReplace, error) {
		if view.Exists {
			if view.Size != 2 {
				return nil, editorSSHNoticeErrorV1()
			}
			var value [2]byte
			if _, err := view.ReaderAt.ReadAt(value[:], 0); err != nil {
				return nil, err
			}
			if !bytes.Equal(value[:], []byte("1\n")) {
				return nil, editorSSHNoticeErrorV1()
			}
			return nil, nil
		}
		if err := display(EditorSSHTrustNotice); err != nil {
			return nil, err
		}
		return func(owned context.Context, out io.Writer) error {
			if err := owned.Err(); err != nil {
				return err
			}
			_, err := io.WriteString(out, "1\n")
			return err
		}, nil
	})
}

func editorSSHNoticeErrorV1() error {
	return &UserError{Msg: "the local editor SSH trust-notice state is unsupported", Hint: "Keep this state unchanged and run a matching " + brand.Name + " client.", Code: "ssh_notice_state", Kind: KindRefused}
}
