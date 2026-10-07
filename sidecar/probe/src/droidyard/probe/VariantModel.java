package droidyard.probe;

import java.util.List;

/** One AGP variant: its name, its build type and its flavours in dimension order. */
public interface VariantModel {

    /** The variant name, lowercase-first, e.g. "paidStagingDebug". */
    String getName();

    /** The build type, e.g. "debug". */
    String getBuildType();

    /** The product flavours in dimension order. */
    List<FlavorModel> getFlavors();
}
