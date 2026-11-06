package process

// deathSignal reports whether a kind's child is asked for SIGTERM when the thread that
// forked it exits: Pdeathsig on Linux, a no-op elsewhere. It is true for every kind
// except SDKTool.
//
// An avdmanager run (SDKTool, plan m3f step 6) keeps the leader alive when the daemon dies,
// so its recorded pid stays valid and the next daemon's Reconcile reaps its whole group,
// which holds the JVM the tool's script started (the shell command and the git write that
// were this case left with ADR-0137 §1). Pdeathsig would kill only the leader; Reconcile
// keys on a live leader, so it would drop the record and leave the rest of the group
// running. Sidecars and device streams keep it: their leader dying with the daemon is what layer 2 of
// process-lifecycle.md §Shutdown, cancellation and no-orphans wants.
func deathSignal(kind Kind) bool {
	return kind != SDKTool
}
