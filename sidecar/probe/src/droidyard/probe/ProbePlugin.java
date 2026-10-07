package droidyard.probe;

import org.gradle.api.Action;
import org.gradle.api.Plugin;
import org.gradle.api.Project;
import org.gradle.api.invocation.Gradle;

/**
 * The probe's entry point, applied by {@code droidyard-probe.init.gradle} to the Gradle
 * object (ADR-0023 §Decision 2). {@code allprojects} is what reaches every project,
 * including the root, and it does so before any of them is configured.
 */
public class ProbePlugin implements Plugin<Gradle> {

    public void apply(Gradle gradle) {
        gradle.allprojects(new Action<Project>() {
            public void execute(Project project) {
                project.getPluginManager().apply(ProbeProjectPlugin.class);
            }
        });
    }
}
