package droidyard.probe;

import java.io.File;
import java.lang.reflect.Method;
import java.util.ArrayList;
import java.util.List;
import javax.inject.Inject;
import org.gradle.api.Action;
import org.gradle.api.Plugin;
import org.gradle.api.Project;
import org.gradle.api.plugins.AppliedPlugin;
import org.gradle.tooling.provider.model.ToolingModelBuilderRegistry;

/**
 * The probe's per-project half (ADR-0023 §Decision 2, plan m1h D6): it registers the
 * {@code ToolingModelBuilder} that answers {@code findModel(project, ProbeModel.class)},
 * and, when an Android plugin is applied, the {@code androidComponents.onVariants}
 * callback plus an {@code afterEvaluate} read of the compile target, the build-tools
 * version and the SDK directory.
 *
 * <strong>Each field is isolated.</strong> Every read sits in its own {@code catch
 * (Throwable)}: a failure omits that field and adds a note, and never fails the model
 * fetch (ADR-0023 §Decision 2). The one deliberate exception is the test hook
 * {@code -Ddroidyard.probe.fail=apply}, which fails the whole plugin outside any
 * isolation so the sidecar's retry path can be exercised.
 */
public class ProbeProjectPlugin implements Plugin<Project> {

    /** The test hook (D5's step 2 half): the field name to fail, or "apply". */
    static final String FAIL_PROPERTY = "droidyard.probe.fail";
    /** Where the project's state is parked for the model builder. */
    static final String STATE_PROPERTY = "droidyardProbeState";

    private static final String[] ANDROID_PLUGINS = {
        "com.android.application",
        "com.android.library",
        "com.android.dynamic-feature",
        "com.android.test",
    };

    private final ToolingModelBuilderRegistry registry;

    @Inject
    public ProbeProjectPlugin(ToolingModelBuilderRegistry registry) {
        this.registry = registry;
    }

    public void apply(final Project project) {
        if ("apply".equals(System.getProperty(FAIL_PROPERTY))) {
            throw new IllegalStateException(
                    "droidyard.probe: the probe's apply failed as asked (droidyard.probe.fail=apply)");
        }
        final ProbeState state = new ProbeState(project.getPath());
        project.getExtensions().getExtraProperties().set(STATE_PROPERTY, state);
        registry.register(new ProbeModelBuilder());
        for (final String id : ANDROID_PLUGINS) {
            project.getPluginManager().withPlugin(id, new Action<AppliedPlugin>() {
                public void execute(AppliedPlugin applied) {
                    onAndroid(project, state);
                }
            });
        }
    }

    private static void onAndroid(final Project project, final ProbeState state) {
        // The Android plugin is applied. from now on a failure of a field is a real
        // loss, not "this is not an Android project".
        state.markAndroid();
        final Object components;
        try {
            components = project.getExtensions().findByName("androidComponents");
        } catch (Throwable t) {
            state.note("androidComponents", t);
            return;
        }
        if (components == null) {
            state.note("androidComponents", new IllegalStateException("the androidComponents extension is not registered"));
            return;
        }
        registerVariants(state, components);
        project.afterEvaluate(new Action<Project>() {
            public void execute(Project evaluated) {
                readAndroid(evaluated, state, components);
            }
        });
    }

    /**
     * Registers the onVariants callback. AGP calls it while the build is configured, so
     * the list is filled long after this returns; the list is registered only when the
     * registration itself succeeded, so a broken registration leaves the variants unread
     * (the field is omitted and the status partial) rather than an empty list.
     */
    private static void registerVariants(final ProbeState state, Object components) {
        final List<VariantModel> variants;
        try {
            Object selector = Reflect.call(components, "selector");
            Object all = Reflect.call(selector, "all");
            Method onVariants = Reflect.actionOverload(components.getClass(), "onVariants");
            if (onVariants == null) {
                throw new NoSuchMethodException("androidComponents.onVariants(selector, Action)");
            }
            variants = state.registerVariants();
            onVariants.invoke(components, all, new Action<Object>() {
                public void execute(Object variant) {
                    collectVariant(state, variants, variant);
                }
            });
        } catch (Throwable t) {
            state.note("variants", t);
        }
    }

    private static void collectVariant(ProbeState state, List<VariantModel> out, Object variant) {
        try {
            fail("variants");
            String name = Reflect.string(variant, "getName");
            String buildType = Reflect.string(variant, "getBuildType");
            List<FlavorModel> flavors = new ArrayList<FlavorModel>();
            List<?> pairs = Reflect.list(variant, "getProductFlavors");
            if (pairs != null) {
                for (Object pair : pairs) {
                    flavors.add(new FlavorBean(Reflect.string(pair, "getFirst"), Reflect.string(pair, "getSecond")));
                }
            }
            out.add(new VariantBean(name, buildType, flavors));
        } catch (Throwable t) {
            state.markVariantsFailed();
            state.note("variants", t);
        }
    }

    private static void readAndroid(Project project, ProbeState state, Object components) {
        final Object android;
        try {
            android = project.getExtensions().findByName("android");
        } catch (Throwable t) {
            state.note("android", t);
            return;
        }
        if (android == null) {
            state.note("android", new IllegalStateException("the android extension is not registered"));
            return;
        }
        state.setCompileTarget(readString(state, "compileTarget", android, "getCompileSdkVersion"));
        state.setBuildTools(readString(state, "buildTools", android, "getBuildToolsVersion"));
        state.setSdkDir(readSdkDir(state, components));
    }

    private static String readString(ProbeState state, String field, Object target, String getter) {
        try {
            fail(field);
            return Reflect.string(target, getter);
        } catch (Throwable t) {
            state.note(field, t);
            return null;
        }
    }

    private static String readSdkDir(ProbeState state, Object components) {
        try {
            fail("sdkDir");
            Object sdkComponents = Reflect.call(components, "getSdkComponents");
            Object directory = Reflect.call(sdkComponents, "getSdkDirectory");
            File resolved = Reflect.file(directory);
            return resolved == null ? null : resolved.getAbsolutePath();
        } catch (Throwable t) {
            state.note("sdkDir", t);
            return null;
        }
    }

    /** Reads the two build-wide versions into the state, each isolated (D6). */
    static void readPlugins(Project project, ProbeState state) {
        try {
            state.setAgp(PluginVersions.agp(project));
        } catch (ClassNotFoundException absent) {
            // No AGP on this project's build classpath: not a failure.
        } catch (NoSuchFieldException absent) {
        } catch (Throwable t) {
            state.note("agp", t);
        }
        try {
            state.setKgp(PluginVersions.kgp(project));
        } catch (ClassNotFoundException absent) {
            // No Kotlin on this project's build classpath: not a failure.
        } catch (NoSuchMethodException absent) {
        } catch (Throwable t) {
            state.note("kgp", t);
        }
    }

    /** The test hook's tripwire, at the head of each isolated read. */
    private static void fail(String field) {
        if (field.equals(System.getProperty(FAIL_PROPERTY))) {
            throw new IllegalStateException("droidyard.probe: the " + field + " read failed as asked");
        }
    }
}
