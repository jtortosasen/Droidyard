package droidyard.probeaction;

import droidyard.probe.ProbeModel;
import java.util.ArrayList;
import java.util.List;
import org.gradle.tooling.BuildAction;
import org.gradle.tooling.BuildController;
import org.gradle.tooling.model.GradleProject;

/**
 * {@code project.model}'s one configuration pass (ADR-0023 §Decision 1, plan m1h D5): it
 * returns the generic model plus, per project, the probe's model.
 *
 * Two properties are load-bearing:
 *
 * <ul>
 *   <li><strong>The action lives in this package, and no class of it is referred to by a
 *       frame the sidecar counts.</strong> The retry rule attributes a failure to the
 *       probe by a frame in {@code droidyard.probe.*} or by a message naming the init
 *       script, so the action's own package is deliberately outside the prefix — else
 *       every configuration failure, which is always under the action's frames, would
 *       look like the probe's and the project's own breakage would be retried.</li>
 *   <li><strong>{@code getModel(GradleProject.class)} is outside any try.</strong> A
 *       project whose own configuration is broken fails there, which is what lets the
 *       sidecar tell "the build is broken" from "the probe is broken".</li>
 * </ul>
 *
 * Each project's {@code findModel} is isolated in its own try: one throwing project is
 * omitted from the probe's facts and noted, and the rest of the fetch goes on.
 */
public class ProbeAction implements BuildAction<ProbeResult> {

    private static final long serialVersionUID = 1L;

    public ProbeResult execute(BuildController controller) {
        GradleProject root = controller.getModel(GradleProject.class);
        List<ProjectFacts> facts = new ArrayList<ProjectFacts>();
        List<String> notes = new ArrayList<String>();
        collect(controller, root, facts, notes);
        return new ProbeResult(root, facts, notes);
    }

    private static void collect(BuildController controller, GradleProject project,
            List<ProjectFacts> facts, List<String> notes) {
        try {
            facts.add(new ProjectFacts(project.getPath(), controller.findModel(project, ProbeModel.class)));
        } catch (Throwable t) {
            String message = t.getMessage();
            notes.add(project.getPath() + ": probe: " + t.getClass().getName() + ": "
                    + (message == null ? "" : message));
        }
        // getChildren() is a DomainObjectSet, not a List: iterate it as-is, in Gradle's
        // own project order (the order the reply's projects[] is built in).
        for (GradleProject child : project.getChildren()) {
            collect(controller, child, facts, notes);
        }
    }
}
