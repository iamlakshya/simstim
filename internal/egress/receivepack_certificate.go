package egress

import "bytes"

func (check *receivePackCheck) certificate(line []byte) error {
	if bytes.IndexByte(line, 0) >= 0 {
		return errReceivePack
	}
	if check.certificateField < 4 {
		prefixes := []string{"certificate version 0.1", "pusher ", "pushee ", "nonce "}
		prefix := []byte(prefixes[check.certificateField])
		if check.certificateField == 0 {
			if !bytes.Equal(line, prefix) {
				return errReceivePack
			}
		} else if !bytes.HasPrefix(line, prefix) || len(line) <= len(prefix) {
			return errReceivePack
		}
		check.certificateField++
		return nil
	}
	if !check.certificateCommands {
		if len(line) == 0 {
			check.certificateCommands = true
			return nil
		}
		if !bytes.HasPrefix(line, []byte("push-option ")) {
			return errReceivePack
		}
		return nil
	}
	if bytes.Equal(line, []byte("push-cert-end")) {
		if check.commands == 0 || !check.signatureEnd {
			return errReceivePack
		}
		check.certificateEnd = true
		return nil
	}
	if check.signature {
		if check.signatureEnd {
			return errReceivePack
		}
		if bytes.Equal(line, []byte("-----END PGP SIGNATURE-----")) {
			check.signatureEnd = true
			return nil
		}
		if bytes.HasPrefix(line, []byte("Version: ")) || bytes.HasPrefix(line, []byte("Comment: ")) {
			for _, value := range line {
				if value < 0x20 || value > 0x7e {
					return errReceivePack
				}
			}
			return nil
		}
		for _, value := range line {
			if !(value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '+' || value == '/' || value == '=') {
				return errReceivePack
			}
		}
		return nil
	} // Cryptographic signature verification belongs to the receiving server.
	if bytes.Equal(line, []byte("-----BEGIN PGP SIGNATURE-----")) {
		if check.commands == 0 {
			return errReceivePack
		}
		check.signature = true
		return nil
	}
	return check.command(line)
}
