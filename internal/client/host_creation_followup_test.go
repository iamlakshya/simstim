package client

import "testing"

func TestCreationFollowupConsumesActualWriterSourceAndRetainsSameOP(t *testing.T) {
	ctx, bound := terminalCreationFixture(t)
	o := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	held := o.creation.op.Load()
	if err := UpdateHosts(ctx, HostChange{Kind: "host create", Now: &bound}); err != nil {
		t.Fatal(err)
	}
	if currentStage(bound.Name) != o.stage.st || sameStage(bound.Name, o.stage.st) != nil {
		t.Fatal("the successful creation writer ended its outer owner's stage before registration")
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
	if currentStage(bound.Name) == o.stage.st || sameStage(bound.Name, o.stage.st) == nil {
		t.Fatal("registration did not supersede the original creator stage")
	}
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
	for _, kind := range []string{"no commit", "new epoch", "new stage"} {
		t.Run(kind, func(t *testing.T) {
			ctx, bound := terminalCreationFixture(t)
			if kind != "no commit" {
				if err := UpdateHosts(ctx, HostChange{Kind: "host create", Now: &bound}); err != nil {
					t.Fatal(err)
				}
				if kind == "new epoch" {
					if err := SaveHosts(t.Context(), []Host{bound}); err != nil {
						t.Fatal(err)
					}
				} else {
					_, abort, err := StagePinOwned(ctx, bound.Name)
					if err != nil {
						t.Fatal(err)
					}
					defer abort()
				}
			}
			EndHostCreationAttemptContext(ctx)
			if _, err := ContinueHostCreationContext(ctx); err == nil {
				t.Fatal("raw result or newerSource became original setup authority")
			}
		})
	}
}
