package process

// deathSignal reports whether a kind's child is asked for SIGTERM when the thread that
// forked it exits: Pdeathsig on Linux, a no-op elsewhere. It is true for every kind
// except ShellCommand and GitWrite.
//
// A shell.exec command keeps the leader alive when the daemon dies, so its recorded pid
// stays valid and the next daemon's Reconcile reaps its whole group (plan m1k D5).
// Pdeathsig would kill only the leader; Reconcile keys on a live leader, so it would
// drop the record and leave the rest of the group running. A git write is the same case
// (plan m2n P2): its group holds the transport a fetch started. Sidecars and device streams
// keep it: their leader dying with the daemon is what layer 2 of
// process-lifecycle.md §Shutdown, cancellation and no-orphans wants.
func deathSignal(kind Kind) bool { return kind != ShellCommand && kind != GitWrite }
