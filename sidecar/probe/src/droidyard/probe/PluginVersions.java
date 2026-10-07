package droidyard.probe;

import java.lang.reflect.Method;
import org.gradle.api.Project;

/**
 * The AGP and KGP versions, read reflectively from the project's build classpath (plan
 * m1h D6; step 1's G1 and G2). Neither plugin's jar is on the probe's compile classpath,
 * so both are reached by name.
 *
 * The classpath to read is the project's own buildscript classloader: a plugin applied
 * with {@code plugins { ... apply false }} at the root, or applied by a sibling, is still
 * there, which is how {@code :app} reports KGP although it does not apply Kotlin, and how
 * AGP 9's built-in Kotlin reports as KGP (step 1's C2).
 */
final class PluginVersions {

    private static final String AGP_VERSION_CLASS = "com.android.Version";
    private static final String AGP_VERSION_FIELD = "ANDROID_GRADLE_PLUGIN_VERSION";
    private static final String KGP_PLUGIN_CLASS = "org.jetbrains.kotlin.gradle.plugin.KotlinPluginWrapperKt";
    private static final String KGP_VERSION_METHOD = "getKotlinPluginVersion";

    private PluginVersions() {
    }

    /**
     * AGP's version constant. A ClassNotFoundException means AGP is not on this project's
     * build classpath at all — the caller treats that as "not there", not as a failure.
     */
    static String agp(Project project) throws Exception {
        ClassLoader loader = project.getBuildscript().getClassLoader();
        Class<?> type = Class.forName(AGP_VERSION_CLASS, false, loader);
        Object value = type.getField(AGP_VERSION_FIELD).get(null);
        return value == null ? null : String.valueOf(value);
    }

    /**
     * KGP's version, through the static accessor step 1 used
     * ({@code KotlinPluginWrapperKt.getKotlinPluginVersion(project)}). The overload is
     * chosen by parameter type, not arity: the same class also declares a {@code Logger}
     * one, and on Gradle 8.0.2 the JVM lists that first, so an arity match passes a
     * Project where a Logger is wanted (step 2's C4). Absent Kotlin is a
     * ClassNotFoundException or a missing method, which the caller treats as "not there".
     */
    static String kgp(Project project) throws Exception {
        ClassLoader loader = project.getBuildscript().getClassLoader();
        Class<?> type = Class.forName(KGP_PLUGIN_CLASS, false, loader);
        Method method = Reflect.findParam(type, KGP_VERSION_METHOD, Project.class);
        if (method == null) {
            throw new NoSuchMethodException(KGP_PLUGIN_CLASS + "." + KGP_VERSION_METHOD + "(Project)");
        }
        Object value = method.invoke(null, project);
        return value == null ? null : String.valueOf(value);
    }
}
