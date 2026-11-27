//go:build !linux

package runtimecache

import (
	"context"

	"github.com/iamlakshya/simstim/internal/supply"
)

type runtimeModeProvisionV1 struct{}

func (*runtimeModeProvisionV1) Close() error                              { return nil }
func (*Store) provisionInstalledVMMV1(context.Context, supply.Item) error { return nil }
