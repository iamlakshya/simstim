package egress

// A containing match hides every source byte of a nested match. Crossing
// echo/minted spans cannot both retain their complete registered identities,
// so that ambiguous response is refused before releasing the overlap.
func (r *responseCredentialReader) checkResponseOverlap(length int, isMint bool) (bool, error) {
	data := r.pending()
	for offset := 1; offset < length; offset++ {
		previous := data[offset-1]
		// A second known echo can start inside either selected span. Keep
		// the WHOLE first span pending until this prefix is decided too.
		echo := r.echo.match(data[offset:], r.eof)
		if echo.wait {
			return true, nil
		}
		if offset+echo.length > length {
			return false, errResponseOverlap
		}
		if !isMint {
			mint := r.rules.match(data[offset:], r.eof, previous, true)
			if mint.wait {
				return true, nil
			}
			if offset+mint.length > length {
				return false, errResponseOverlap
			}
		}
	}
	return false, nil
}
