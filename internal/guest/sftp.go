package guest

import (
	"context"
	"os"

	"github.com/pkg/sftp"

	"github.com/iamlakshya/simstim/internal/link"
)

func (r *runner) handleExtraStream(ctx context.Context, st *link.Stream) {
	switch st.Kind() {
	case link.KindSFTP:
		r.serveSFTP(ctx, st)
	case link.KindExec:
		r.serveExec(ctx, st)
	default:
		_ = st.Close()
	}
}

// sessionContext returns the workspace directory and sanitized guest
// environment for sessionID, falling back to the primary session when
// sessionID is empty.
func (r *runner) sessionContext(sessionID string) (cwd string, env []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sessionID != "" {
		if mh, ok := r.holders[sessionID]; ok {
			return mh.cwd, append([]string(nil), mh.env...)
		}
	}
	for _, mh := range r.holders {
		return mh.cwd, append([]string(nil), mh.env...)
	}
	return "", nil
}

// serveSFTP serves the SFTP protocol over a link.KindSFTP stream inside the
// sandbox as the guest user (agent), rooted at the session's workspace
// (isolation spec 17.7, 21.1).
func (r *runner) serveSFTP(ctx context.Context, st *link.Stream) {
	defer st.Close()
	var hdr SFTPHeader
	_ = st.Header(&hdr)

	workDir, _ := r.sessionContext(hdr.Session)
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	var opts []sftp.ServerOption
	if workDir != "" {
		opts = append(opts, sftp.WithServerWorkingDirectory(workDir))
	}
	srv, err := sftp.NewServer(st, opts...)
	if err != nil {
		return
	}
	defer srv.Close()

	stop := context.AfterFunc(ctx, func() {
		_ = srv.Close()
		_ = st.Close()
	})
	defer stop()

	_ = srv.Serve()
}
