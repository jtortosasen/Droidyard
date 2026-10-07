package droidyard.probe;

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

/**
 * One project's probe state: what was read, what failed, and the notes. It lives in the
 * project's extra properties for the project plugin's lifetime; the model builder turns
 * it into {@link ProbeModelBean}.
 *
 * Notes are capped the way the plan asks (D6): at most 50, each cut at 512 characters, so
 * a pathological project cannot grow the model without bound.
 */
final class ProbeState {

    private static final int MAX_NOTES = 50;
    private static final int MAX_NOTE_CHARS = 512;

    private final String path;
    private volatile boolean android;
    private volatile String agp;
    private volatile String kgp;
    private volatile String compileTarget;
    private volatile String buildTools;
    private volatile String sdkDir;
    /** The variants onVariants collected; null while it was never registered. */
    private volatile List<VariantModel> variants;
    private final List<String> notes = Collections.synchronizedList(new ArrayList<String>());

    ProbeState(String path) {
        this.path = path;
    }

    void markAndroid() {
        android = true;
    }

    boolean isAndroid() {
        return android;
    }

    String getAgp() {
        return agp;
    }

    void setAgp(String value) {
        agp = value;
    }

    String getKgp() {
        return kgp;
    }

    void setKgp(String value) {
        kgp = value;
    }

    String getCompileTarget() {
        return compileTarget;
    }

    void setCompileTarget(String value) {
        compileTarget = value;
    }

    String getBuildTools() {
        return buildTools;
    }

    void setBuildTools(String value) {
        buildTools = value;
    }

    String getSdkDir() {
        return sdkDir;
    }

    void setSdkDir(String value) {
        sdkDir = value;
    }

    /**
     * Set when one variant could not be read: a list missing an entry is not exact, so the
     * field is omitted and the core falls back to task names (D6; step 2 review B2).
     */
    private volatile boolean variantsFailed;

    void markVariantsFailed() {
        variantsFailed = true;
    }

    /** The variant list, or null when onVariants was never registered or a variant failed. */
    List<VariantModel> getVariants() {
        return variantsFailed ? null : variants;
    }

    /**
     * Records that onVariants was registered: the returned list stays live, because AGP
     * calls the callback while the build is configured, well after registration returns.
     */
    List<VariantModel> registerVariants() {
        List<VariantModel> list = Collections.synchronizedList(new ArrayList<VariantModel>());
        variants = list;
        return list;
    }

    /** A field's failure, in the plan's form: "&lt;project path&gt;: &lt;field&gt;: &lt;class&gt;: &lt;message&gt;". */
    void note(String field, Throwable failure) {
        String message = failure.getMessage();
        note(path + ": " + field + ": " + failure.getClass().getName() + ": "
                + (message == null ? "" : message));
    }

    /** A note that is already complete. */
    void note(String text) {
        synchronized (notes) {
            if (notes.size() >= MAX_NOTES) {
                return;
            }
            notes.add(cut(text));
        }
    }

    List<String> getNotes() {
        synchronized (notes) {
            return new ArrayList<String>(notes);
        }
    }

    private static String cut(String text) {
        return text.length() <= MAX_NOTE_CHARS ? text : text.substring(0, MAX_NOTE_CHARS);
    }
}
