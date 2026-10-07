package droidyard.probe;

import java.io.Serializable;
import java.util.List;

/**
 * The daemon side of {@link ProbeModel}: a concrete, Serializable bean whose getters
 * carry the same names, which is what the Tooling API maps onto the client's proxy
 * (ADR-0023 §Decision 2).
 *
 * Every nested value is a concrete bean of its own and every list is an ArrayList: an
 * anonymous or non-Serializable class anywhere in the tree fails the result's
 * serialization to the client, not the model fetch, so it would be a silent field loss.
 */
public class ProbeModelBean implements ProbeModel, Serializable {

    private static final long serialVersionUID = 1L;

    private final String agp;
    private final String kgp;
    private final AndroidBean android;
    private final List<String> notes;

    public ProbeModelBean(String agp, String kgp, AndroidBean android, List<String> notes) {
        this.agp = agp;
        this.kgp = kgp;
        this.android = android;
        this.notes = notes;
    }

    public String getAgp() {
        return agp;
    }

    public String getKgp() {
        return kgp;
    }

    public AndroidModel getAndroid() {
        return android;
    }

    public List<String> getNotes() {
        return notes;
    }
}
