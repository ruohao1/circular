package postgres

// Keep the admission fixture independent of queue claiming and process execution.
var AdmitApprovedDelegationForTest = (*Queue).admitApprovedDelegation
