package droidyard.probe;

import java.io.Serializable;
import java.util.List;

/** The daemon side of {@link AndroidModel}; see {@link ProbeModelBean}. */
public class AndroidBean implements AndroidModel, Serializable {

    private static final long serialVersionUID = 1L;

    private final String compileTarget;
    private final String buildTools;
    private final String sdkDir;
    private final List<VariantModel> variants;

    public AndroidBean(String compileTarget, String buildTools, String sdkDir, List<VariantModel> variants) {
        this.compileTarget = compileTarget;
        this.buildTools = buildTools;
        this.sdkDir = sdkDir;
        this.variants = variants;
    }

    public String getCompileTarget() {
        return compileTarget;
    }

    public String getBuildTools() {
        return buildTools;
    }

    public String getSdkDir() {
        return sdkDir;
    }

    public List<VariantModel> getVariants() {
        return variants;
    }
}
