//go:build !linux

package publicationlease

import (
	"context"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

type SourceV1 struct{}
type HolderV1 struct{}
type ExclusiveV1 struct{}

func unsupportedV1() error { return failureV1("the original publication fence requires Linux") }
func CreateV1(context.Context, *os.Root, cachefs.Domain, string) (*SourceV1, error) {
	return nil, unsupportedV1()
}
func CreateReservedV1(context.Context, *os.Root, cachefs.Domain, string, func(string) error) (*SourceV1, error) {
	return nil, unsupportedV1()
}
func (*SourceV1) RecordV1() string                                    { return "" }
func (*SourceV1) CheckV1(context.Context) error                       { return unsupportedV1() }
func (*SourceV1) RetainForHelperV1(context.Context) (*os.File, error) { return nil, unsupportedV1() }
func (*SourceV1) FreezeParentV1() error                               { return unsupportedV1() }
func (*SourceV1) ProbeV1(context.Context) (*ExclusiveV1, error)       { return nil, unsupportedV1() }
func (*SourceV1) CloseUnpublishedV1() error                           { return unsupportedV1() }
func (*SourceV1) FileInfoV1() (os.FileInfo, error)                    { return nil, unsupportedV1() }
func (*SourceV1) CloseV1() error                                      { return unsupportedV1() }
func TryExclusiveV1(context.Context, *os.Root, cachefs.Domain, string) (*ExclusiveV1, error) {
	return nil, unsupportedV1()
}
func ResumeRemovalV1(context.Context, *os.Root, cachefs.Domain, string) (*ExclusiveV1, error) {
	return nil, unsupportedV1()
}
func (*ExclusiveV1) CheckV1() error         { return unsupportedV1() }
func (*ExclusiveV1) RemoveV1() error        { return unsupportedV1() }
func (*ExclusiveV1) RemovalDoneV1() bool    { return false }
func (*ExclusiveV1) CloseV1() error         { return nil }
func CaptureGuardianV1() (*HolderV1, error) { return nil, unsupportedV1() }
func (*HolderV1) RetainForJailV1(context.Context) (*os.File, string, error) {
	return nil, "", unsupportedV1()
}
func (*HolderV1) CloseV1() error              { return nil }
func CheckInheritedV1(*os.File, string) error { return unsupportedV1() }

func CheckInheritedFDV1(int, string) error { return unsupportedV1() }
func CheckConverterFDV1(int) error         { return unsupportedV1() }
