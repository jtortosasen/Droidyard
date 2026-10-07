package droidyard.probeaction;

import droidyard.probe.ProbeModel;
import java.io.Serializable;

/**
 * One project's facts: its path and the probe's model for it. The model is null when the
 * project had none to give (it failed its own findModel, or the probe registered no
 * builder for it).
 */
public class ProjectFacts implements Serializable {

    private static final long serialVersionUID = 1L;

    private final String path;
    private final ProbeModel model;

    public ProjectFacts(String path, ProbeModel model) {
        this.path = path;
        this.model = model;
    }

    public String getPath() {
        return path;
    }

    public ProbeModel getModel() {
        return model;
    }
}
