package api

// FeatureVMNeedV1 guards the same-request VM Need envelope and its actual
// authenticated/granted producer. A helper advertises it only with that live
// path; the data type or a downloaded runtime alone does not offer support.
const FeatureVMNeedV1 = "vm-need-v1"
