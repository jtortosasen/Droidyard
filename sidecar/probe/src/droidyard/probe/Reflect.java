package droidyard.probe;

import java.io.File;
import java.lang.reflect.Method;
import java.util.ArrayList;
import java.util.List;

/**
 * Reflection helpers. The probe is compiled against Gradle 8.0.2's public API alone
 * (plan m1h D4), so AGP's and KGP's own methods are not on its compile classpath: every
 * one of those calls goes through here.
 *
 * Public methods are reached through {@link Class#getMethods()}, which finds an inherited
 * or interface-declared one, and made accessible first: AGP's and KGP's implementations
 * are non-public classes with public methods, which a plain invoke refuses. The classes
 * sit in the unnamed module on the build classpath, so {@code setAccessible} is granted.
 */
final class Reflect {

    private Reflect() {
    }

    /** The public method named name with arity parameters, made accessible; null if none. */
    static Method find(Class<?> type, String name, int arity) {
        for (Method m : type.getMethods()) {
            if (m.getName().equals(name) && m.getParameterCount() == arity) {
                m.setAccessible(true);
                return m;
            }
        }
        return null;
    }

    /**
     * The public method named name whose one parameter accepts an instance of param. KGP's
     * {@code getKotlinPluginVersion} has both a {@code Project} and a {@code Logger}
     * overload and {@link Class#getMethods()} has no defined order, so a plain arity match
     * picks whichever the JVM happens to list first — the Logger one on Gradle 8.0.2,
     * where passing a Project then fails with "argument type mismatch" (step 2's C4). The
     * parameter type is what decides; null if no overload takes one.
     */
    static Method findParam(Class<?> type, String name, Class<?> param) {
        for (Method m : type.getMethods()) {
            if (m.getName().equals(name) && m.getParameterCount() == 1
                    && m.getParameterTypes()[0].isAssignableFrom(param)) {
                m.setAccessible(true);
                return m;
            }
        }
        return null;
    }

    /**
     * The two-argument overload of name (onVariants) whose last parameter is a Gradle
     * Action. AGP declares both an Action and a Kotlin Function1 overload; taking the
     * Action one keeps kotlin.jvm.functions.Function1 out of the probe's constant pool,
     * which the jar's no-Kotlin test asserts.
     */
    static Method actionOverload(Class<?> type, String name) {
        Class<?> action;
        try {
            action = Class.forName("org.gradle.api.Action");
        } catch (ClassNotFoundException e) {
            return null;
        }
        for (Method m : type.getMethods()) {
            if (m.getName().equals(name) && m.getParameterCount() == 2
                    && action.isAssignableFrom(m.getParameterTypes()[1])) {
                m.setAccessible(true);
                return m;
            }
        }
        return null;
    }

    static Object call(Object target, String name) throws ReflectiveOperationException {
        Method m = find(target.getClass(), name, 0);
        if (m == null) {
            throw new NoSuchMethodException(target.getClass().getName() + "." + name + "()");
        }
        return m.invoke(target);
    }

    /** A no-argument getter's value as a String; null stays null. */
    static String string(Object target, String name) throws ReflectiveOperationException {
        Object v = call(target, name);
        return v == null ? null : String.valueOf(v);
    }

    /** A no-argument getter's value as a List; a non-List Iterable is copied; else null. */
    static List<?> list(Object target, String name) throws ReflectiveOperationException {
        Object v = call(target, name);
        if (v instanceof List) {
            return (List<?>) v;
        }
        if (v instanceof Iterable) {
            List<Object> out = new ArrayList<Object>();
            for (Object item : (Iterable<?>) v) {
                out.add(item);
            }
            return out;
        }
        return null;
    }

    /**
     * The File a Gradle Directory, a {@code Provider<Directory>} or a File names. AGP
     * hands the SDK directory out as a lazy provider, so a provider is resolved first.
     */
    static File file(Object value) throws ReflectiveOperationException {
        Object v = value;
        if (v == null || v instanceof File) {
            return (File) v;
        }
        Method get = find(v.getClass(), "get", 0);
        if (get != null) {
            Object resolved = get.invoke(v);
            if (resolved != null) {
                v = resolved;
            }
        }
        if (v instanceof File) {
            return (File) v;
        }
        Method asFile = find(v.getClass(), "getAsFile", 0);
        if (asFile != null) {
            Object f = asFile.invoke(v);
            if (f instanceof File) {
                return (File) f;
            }
        }
        return null;
    }
}
