package droidyard.probe;

import java.io.Serializable;
import java.util.List;

/** The daemon side of {@link VariantModel}; see {@link ProbeModelBean}. */
public class VariantBean implements VariantModel, Serializable {

    private static final long serialVersionUID = 1L;

    private final String name;
    private final String buildType;
    private final List<FlavorModel> flavors;

    public VariantBean(String name, String buildType, List<FlavorModel> flavors) {
        this.name = name;
        this.buildType = buildType;
        this.flavors = flavors;
    }

    public String getName() {
        return name;
    }

    public String getBuildType() {
        return buildType;
    }

    public List<FlavorModel> getFlavors() {
        return flavors;
    }
}
