package host

// Hooks for the tests of package host_test.
var (
	// StartHolder lets tests observe only children this launch created.
	StartHolder = &startHolder
	// AfterNameCheck is afterNameCheck, for a test that tries a rename
	// inside a guarded delete's lock.
	AfterNameCheck = &afterNameCheck
	// Deliver is deliver, for a test whose delivery of the secrets fails.
	Deliver = &deliver
	// SignalHolder is signalHolder, for a test whose holder will not stop.
	SignalHolder = &signalHolder
	// StopWait is stopWait, for a test that waits for a holder that will
	// not stop.
	StopWait = &stopWait
	// RenameWait is renameWait, for a test of a holder that does not
	// answer.
	RenameWait = &renameWait
	// StartWait is startWait, for a test of a holder that answers late.
	StartWait = &startWait
	// LateStart is lateStart, for a test whose holder starts its agent
	// after the start gave up waiting.
	LateStart = &lateStart
	// RenameSaveWait is how long a rename waits for an older holder's save.
	RenameSaveWait = &renameSaveWait
	// RestoreWait is how much longer a restore waits for a slow holder.
	RestoreWait = &restoreWait
	// DrawID draws one candidate session ID.
	DrawID = &drawID
	// HolderFeatures are what a holder of this helper names in its hello.
	HolderFeatures = holderFeatures
	// HolderEnded is holderEnded.
	HolderEnded = holderEnded
	// HolderLockHeld is holderLockHeld.
	HolderLockHeld = holderLockHeld
	// HolderPIDAlive checks a recorded process without consuming its exit.
	HolderPIDAlive = holderPIDAlive
	// HolderStopWaiting runs while an acknowledged holder stop finishes.
	HolderStopWaiting = &holderStopWaiting
	// BeforeConfirmRename runs after an older holder answers a rename,
	// before confirmRename runs.
	BeforeConfirmRename = &beforeConfirmRename
	// WriteUnsavedFile writes raw bytes to rename.unsaved.
	WriteUnsavedFile = &writeUnsavedFile
	// WritePendingManifest writes a slow restored holder's manifest record.
	WritePendingManifest = &writePendingManifest
)

// UnitEnv is the variable that names a holder's unit.
var UnitEnv = unitEnv
