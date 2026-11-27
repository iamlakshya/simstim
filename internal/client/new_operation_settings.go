package client

import "context"

func newOperationSettings(ctx context.Context) (Settings, error) {
	r := grantRequestOf(ctx)
	if r == nil {
		return ReadPolicySettings()
	}
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	return operation.Settings(ctx)
}

func requestPolicySettings(ctx context.Context) (Settings, error) {
	if r := grantRequestOf(ctx); r != nil && requestGrantOperation(r) == grantOpNew {
		return newOperationSettings(ctx)
	}
	return ReadPolicySettings()
}
