package supply

// ExecModeLeaseV1 is issued only from an actual checked native runtime owner.
// Its zero value, copied metadata and native-reference snapshots cannot
// attach a mode phase to a jail. File handles it lends are separate private
// capabilities, not ordinary data or executable grants.
type ExecModeLeaseV1 struct{ state *execModeStateV1 }
