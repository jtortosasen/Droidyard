package droidyard.probeaction;

import java.io.Serializable;
import java.util.List;
import org.gradle.tooling.model.GradleProject;

/**
 * The action's result: the generic model it fetched (the {@code GradleProject} tree), the
 * probe's facts per project, and the notes from the projects that could not answer.
 *
 * Every field is a concrete Serializable type and every list is an ArrayList: an
 * anonymous class anywhere in this tree fails its serialization to the client with a
 * NotSerializableException, which would turn the whole probed fetch into an error.
 */
public class ProbeResult implements Serializable {

    private static final long serialVersionUID = 1L;

    private final GradleProject root;
    private final List<ProjectFacts> facts;
    private final List<String> notes;

    public ProbeResult(GradleProject root, List<ProjectFacts> facts, List<String> notes) {
        this.root = root;
        this.facts = facts;
        this.notes = notes;
    }

    public GradleProject getRoot() {
        return root;
    }

    public List<ProjectFacts> getFacts() {
        return facts;
    }

    public List<String> getNotes() {
        return notes;
    }
}
