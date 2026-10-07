package droidyard.probe;

import java.util.List;

/**
 * One Android project's probe facts (plan m1h D6): the compile target string, the
 * effective build-tools version, the SDK directory AGP resolved and the variants from
 * {@code androidComponents.onVariants}. Each field is null when its own read failed.
 */
public interface AndroidModel {

    /** AGP's compile target hash string, e.g. "android-36". */
    String getCompileTarget();

    /** AGP's effective build-tools version, explicit or its own default. */
    String getBuildTools();

    /** The SDK directory AGP resolved. Core-internal: it never reaches a project.* result. */
    String getSdkDir();

    /** The variants in AGP's order, build type and flavours included; null when unread. */
    List<VariantModel> getVariants();
}
