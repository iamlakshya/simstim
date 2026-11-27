package client

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/proto"
)

func imageSaveProtocolError() error {
	return &UserError{Code: "image_protocol", Kind: KindFailed,
		Msg: "the host returned an image save outside the original review", Hint: "Upgrade the host helper and inspect image ls before saving again."}
}

func imageSaveClientError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var user *UserError
	if errors.As(err, &user) {
		return err
	}
	var remote *proto.Error
	if errors.As(err, &remote) {
		return &UserError{Code: remote.Code, Msg: CleanLine(remote.Message), Hint: CleanLine(remote.Hint), Kind: HostKind(remote.Code)}
	}
	return &UserError{Code: "image_save", Kind: KindFailed,
		Msg: "could not finish the image save: " + CleanLine(err.Error()), Hint: "Check the host connection and inspect image ls before saving again."}
}
