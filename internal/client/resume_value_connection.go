package client

import "context"

// A host row cannot reconstruct the connection which prepared this resume.
// Legacy unwalled calls retain their existing path; protected cold values use
// only the actual same logical request's retained native-preparation owner.
func originalResumeValueConnectionV1(ctx context.Context, host Host, id string) (*Conn, error) {
	r := grantRequestOf(ctx)
	if r == nil {
		return nil, grantOperationError()
	}
	r.mu.Lock()
	named := r.namedResume
	r.mu.Unlock()
	if named == nil {
		return nil, nil
	}
	if named.conn == nil || named.id != id || !SameEntry(named.conn.Host, host) {
		return nil, grantOperationError()
	}
	if err := bindRequestTransportSource(ctx, named.conn); err != nil {
		return nil, err
	}
	return named.conn, nil
}
