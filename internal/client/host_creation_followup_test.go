package client

import "testing"

func TestCreationFollowupConsumesActualWriterSourceAndRetainsSameOP(t *testing.T) {
	ctx, bound := terminalCreationFixture(t)
	o := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	held := o.creation.op.Load()
	if err := UpdateHosts(ctx, HostChange{Kind: "host create", Now: &bound}); err != nil {
		t.Fatal(err)
	}
	EndHostCreationAttemptContext(ctx)
	post, err := ContinueHostCreationContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	read := post.Value(hostStateObservationKey{}).(*HostStateObservation)
	if read.role != hostStateRead || read.op != held || !read.source.exists || read.source.generation == o.source.generation || read.hasCheckedIdentity() {
		t.Fatal("followup lost actual new Source/OP or invented checked authority")
	}
	if _, err := ContinueHostCreationContext(ctx); err == nil {
		t.Fatal("creator commit proof donated multiple setup transitions")
	}
	stage, abort, err := StagePinOwned(post, bound.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	registration, err := ObserveHostRegistrationContext(stage, HostChange{Kind: "host add", Was: &bound, Now: &bound})
	if err != nil {
		t.Fatal(err)
	}
	if !sameHostTransportSource(registration.source, read.source) {
		t.Fatal("owned registration recaptured another Source around creator data")
	}
	if held.gone.Load() {
		t.Fatal("transition released outer OP")
	}
}

func TestCreationFollowupRefusesMissingCommitAndChangedSource(t *testing.T) {
	for _, kind := range []string{"no commit", "new epoch"} {
		t.Run(kind, func(t *testing.T) {
			ctx, bound := terminalCreationFixture(t)
			if kind == "new epoch" {
				if err := UpdateHosts(ctx, HostChange{Kind: "host create", Now: &bound}); err != nil {
					t.Fatal(err)
				}
				if err := SaveHosts(t.Context(), []Host{bound}); err != nil {
					t.Fatal(err)
				}
			}
			EndHostCreationAttemptContext(ctx)
			if _, err := ContinueHostCreationContext(ctx); err == nil {
				t.Fatal("raw result or newerSource became original setup authority")
			}
		})
	}
}
