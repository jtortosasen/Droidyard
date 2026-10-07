package droidyard.probe;

import org.gradle.api.Project;
import org.gradle.tooling.provider.model.ToolingModelBuilder;

/**
 * Answers the client's {@code findModel(project, ProbeModel.class)} (ADR-0023 §Decision 2).
 * The project plugin registers one of these per project; the action asks each project for
 * its model, so a project whose own configuration is broken is the one that fails — never
 * the whole fetch.
 */
public class ProbeModelBuilder implements ToolingModelBuilder {

    public boolean canBuild(String modelName) {
        return ProbeModel.class.getName().equals(modelName);
    }

    public Object buildAll(String modelName, Project project) {
        Object stored = project.getExtensions().getExtraProperties().get(ProbeProjectPlugin.STATE_PROPERTY);
        if (!(stored instanceof ProbeState)) {
            return null;
        }
        ProbeState state = (ProbeState) stored;
        // Read here, not at apply time: the project's build classpath is populated once
        // its build script is evaluated, and this runs after the whole configuration.
        ProbeProjectPlugin.readPlugins(project, state);
        AndroidBean android = null;
        if (state.isAndroid()) {
            android = new AndroidBean(state.getCompileTarget(), state.getBuildTools(),
                    state.getSdkDir(), state.getVariants());
        }
        return new ProbeModelBean(state.getAgp(), state.getKgp(), android, state.getNotes());
    }
}
