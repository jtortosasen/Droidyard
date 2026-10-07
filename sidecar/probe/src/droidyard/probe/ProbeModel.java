package droidyard.probe;

import java.util.List;

/**
 * The probe's model for one project: what a {@code findModel(project, ProbeModel.class)}
 * asks for on the client and what the daemon's builder maps (ADR-0023 §Decision 2; plan
 * m1h D6). It is a plain interface on purpose — no Gradle type appears in it, so the
 * client (the sidecar) can load it without Gradle's API on its classpath.
 *
 * A field the probe could not read is null, and its failure is in {@link #getNotes}.
 */
public interface ProbeModel {

    /** The AGP version on the project's build classpath, or null when AGP is not there. */
    String getAgp();

    /** The KGP version, or null when Kotlin is not on the project's build classpath. */
    String getKgp();

    /** The Android facts, or null when the project does not apply an Android plugin. */
    AndroidModel getAndroid();

    /** This project's probe notes, each "&lt;path&gt;: &lt;field&gt;: &lt;class&gt;: &lt;message&gt;". */
    List<String> getNotes();
}
