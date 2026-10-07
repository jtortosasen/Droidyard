package droidyard.probe;

/** One product flavour of a variant: the dimension it belongs to and its name. */
public interface FlavorModel {

    /** The flavour dimension, e.g. "tier". */
    String getDimension();

    /** The flavour name, e.g. "paid". */
    String getName();
}
