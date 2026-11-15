package egress

import (
	"net"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/secretbind"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

func (s *fdPublicState) authority(whole, window api.JSONSpan) (string, error) {
	validator := secretbind.NewAuthorityValidator(s.ctx)
	var scratch [utf8.UTFMax]byte
	err := s.p.WalkStringEncodedWindow(whole, window, func(r rune) error { _, err := validator.Write(utf8.AppendRune(scratch[:0], r)); return err })
	if err != nil {
		return "", err
	}
	return validator.Finish()
}

func (s *fdPublicState) binding(name, span api.JSONSpan, historical bool) (uint64, error) {
	var header, format, hosts api.JSONSpan
	err := fdFields(s.p, span, s.schema.binding, func(key string, value api.JSONSpan) error {
		switch key {
		case "header":
			header = value
		case "format":
			format = value
		case "hosts":
			hosts = value
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	headerCheck := secretbind.NewHeaderValidator(s.ctx)
	var headerBytes uint64
	err = fdWriteString(s.p, header, func(part []byte) (int, error) {
		if headerBytes > ^uint64(0)-uint64(len(part)) {
			return 0, invalidPublicSessionState()
		}
		headerBytes += uint64(len(part))
		return headerCheck.Write(part)
	})
	if err != nil {
		return 0, err
	}
	if err := headerCheck.Finish(); err != nil {
		return 0, err
	}
	formatCheck := secretbind.NewFormatValidator(s.ctx)
	if err := fdWriteString(s.p, format, formatCheck.Write); err != nil {
		return 0, err
	}
	kind, userBytes, err := formatCheck.FinishFacts()
	if err != nil {
		return 0, err
	}
	id, err := s.region.AppendBinding(tomlinspect.BindingRecord{Name: fdSpan(name), Header: fdSpan(header), Format: fdSpan(format), FormatKind: fdFormatKind(kind), UserBytes: userBytes, HeaderBytes: headerBytes})
	if err != nil {
		return 0, err
	}
	var count uint64
	err = projectedStoredArray(s.p, hosts, func(whole api.JSONSpan) error {
		validator := secretbind.NewAuthorityValidator(s.ctx)
		window := api.JSONSpan{Start: whole.Start + 1, End: whole.Start + 1}
		started := false
		var scratch [utf8.UTFMax]byte
		err := s.p.WalkStringSpans(whole, func(r rune, encoded api.JSONSpan) error {
			if !unicode.IsSpace(r) {
				if !started {
					window.Start = encoded.Start
					started = true
				}
				window.End = encoded.End
			}
			_, err := validator.Write(utf8.AppendRune(scratch[:0], r))
			return err
		})
		if err != nil {
			return err
		}
		authority, err := validator.Finish()
		if err != nil {
			return err
		}
		if !historical {
			host, _, err := net.SplitHostPort(authority)
			if err != nil {
				return err
			}
			_, splice, err := httpClass(host)
			if err != nil || splice {
				return invalidPublicSessionState()
			}
		}
		hostID, err := s.region.AppendHost(tomlinspect.HostRecord{BindingID: id, WholeSpan: fdSpan(whole), InteriorSpan: fdSpan(window)})
		if err != nil {
			return err
		}
		inserted, err := s.hosts.Put(id, hostID, hostID)
		if err != nil {
			return err
		}
		if inserted {
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, invalidPublicSessionState()
	}
	if err := s.region.SetBindingHostCount(id, count); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *fdPublicState) hostCompare(group uint64, a uint64, _ uint64, b uint64) (int, error) {
	x, err := s.region.Host(a)
	if err != nil {
		return 0, err
	}
	y, err := s.region.Host(b)
	if err != nil {
		return 0, err
	}
	binding, err := s.region.Binding(group)
	if err != nil {
		return 0, err
	}
	if binding.FormatKind == fdNameMapKind {
		return s.p.CompareStrings(jsonSpan(x.WholeSpan), jsonSpan(y.WholeSpan))
	}
	if binding.FormatKind < 1 || binding.FormatKind > 3 {
		return 0, tomlinspect.ErrIndexInvariant
	}
	left, err := s.authority(jsonSpan(x.WholeSpan), jsonSpan(x.InteriorSpan))
	if err != nil {
		return 0, err
	}
	right, err := s.authority(jsonSpan(y.WholeSpan), jsonSpan(y.InteriorSpan))
	if err != nil {
		return 0, err
	}
	return strings.Compare(left, right), nil
}

func (s *fdPublicState) sameBinding(a, b uint64) (bool, error) {
	if a == 0 || b == 0 {
		return a == b, nil
	}
	x, err := s.region.Binding(a)
	if err != nil {
		return false, err
	}
	y, err := s.region.Binding(b)
	if err != nil {
		return false, err
	}
	if x.FormatKind != y.FormatKind || x.UserBytes != y.UserBytes || x.HeaderBytes != y.HeaderBytes || x.HostCount != y.HostCount {
		return false, nil
	}
	order, err := fdCompareStrings(s.p, jsonSpan(x.Header), jsonSpan(y.Header), true)
	if err != nil || order != 0 {
		return false, err
	}
	order, err = s.p.CompareStrings(jsonSpan(x.Format), jsonSpan(y.Format))
	if err != nil || order != 0 {
		return false, err
	}
	equal := true
	err = s.hosts.WalkFirst(a, func(_ uint64, hostID, _ uint64) error {
		_, found, err := s.hosts.Find(b, hostID)
		if err != nil {
			return err
		}
		equal = equal && found
		return nil
	})
	return equal, err
}
