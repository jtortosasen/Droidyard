package droidyard.sidecar

import java.io.FileDescriptor
import java.io.FileOutputStream
import java.io.PrintStream

/**
 * The Gradle Tooling API sidecar (ADR-0017, plan m1g step 1). It speaks JSON-RPC 2.0,
 * one message per line, on standard input and standard output; standard error is the
 * log sink.
 *
 * stdout carries the protocol and nothing else (D6): the original standard output is
 * captured for the protocol, then `System.out` is pointed at stderr, so a stray
 * `println` — ours, the Tooling API's, or a build plugin's — goes to the log sink
 * instead of corrupting a protocol line.
 */
fun main(args: Array<String>) {
    val protocolStream = FileOutputStream(FileDescriptor.out)
    System.setOut(PrintStream(System.err, true))
    Session(Protocol(protocolStream)).serve()
}
