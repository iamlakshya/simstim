package host

const ordinaryStopRoleV1 = "ordinary-stop-v1"
const ordinaryHolderRoleV1 = "ordinary-holder-v1"
const ordinaryStopInspectV1 = "ordinary_stop_inspect_v1"
const ordinaryStopCommitV1 = "ordinary_stop_commit_v1"

// Correlation only. The actual native producer, private holder OFD and
// original creation pidfds are required separately at every consumer.
type ordinaryStopStateV1 struct {
	Version    int    `json:"v"`
	Primary    string `json:"primary"`
	Nonce      string `json:"nonce"`
	Generation string `json:"generation"`
}

const ordinaryPurgeRoleV2 = "ordinary-purge-v2"
const ordinaryPurgeHolderRoleV2 = "ordinary-purge-holder-v2"
const ordinaryPurgeCheckV2 = "ordinary_purge_check_v2"
const ordinaryPurgeQuiesceV2 = "ordinary_purge_quiesce_v2"
const ordinaryPurgeCheckQuiescedV2 = "ordinary_purge_check_quiesced_v2"
const ordinaryPurgeRestoreV2 = "ordinary_purge_restore_v2"
