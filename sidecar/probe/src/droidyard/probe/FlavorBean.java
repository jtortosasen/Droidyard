package droidyard.probe;

import java.io.Serializable;

/** The daemon side of {@link FlavorModel}; see {@link ProbeModelBean}. */
public class FlavorBean implements FlavorModel, Serializable {

    private static final long serialVersionUID = 1L;

    private final String dimension;
    private final String name;

    public FlavorBean(String dimension, String name) {
        this.dimension = dimension;
        this.name = name;
    }

    public String getDimension() {
        return dimension;
    }

    public String getName() {
        return name;
    }
}
