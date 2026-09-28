# Boceto de proyecto: suite ligera de desarrollo Android orientada a agentes

## 1. Objetivo

Construir una suite propia para desarrollo Android que permita sustituir en gran medida Android Studio sin necesidad de arrancar Android Studio/IntelliJ como IDE.

La idea no es crear otro IDE pesado, sino un **runtime ligero de desarrollo Android + UI web + capa de integración para agentes de código**.

El sistema debe funcionar principalmente en PC, pero la interfaz debe poder abrirse también desde una tablet mediante navegador.

Prioridad general: **poco consumo de RAM, arquitectura modular, integración fuerte con herramientas Android existentes y buena exposición para agentes de IA**.

---

## 2. Qué debería cubrir la suite

### UI / experiencia humana

- Explorador de proyecto.
- Visualización y navegación del código.
- Editor ligero en navegador.
- Ir a definición.
- Buscar referencias/usos.
- Símbolos del archivo/proyecto.
- Diagnósticos.
- Visualización de Git: status, diff, commits/branch, etc.
- Lanzar tareas Gradle con botones y/o terminal.
- Visualización de dispositivos conectados.
- Gestión de emuladores.
- ADB.
- Logcat.
- Procesos y estado de la app.
- Capturas de pantalla y, potencialmente, stream de pantalla.
- Información de build/test.
- Panel de problemas/errores.

No se busca competir con todas las capacidades de Android Studio. Se busca cubrir de forma correcta y ligera las capacidades realmente útiles para el desarrollo asistido por IA.

---

## 3. Arquitectura conceptual

Propuesta base:

```text
                    Browser
              (PC / tablet / móvil)
                       |
                 HTTP / WebSocket
                       |
                       v
                +--------------+
                |    mydev     |
                | native core  |
                +--------------+
                  |     |     |
                  |     |     +----------------+
                  |     |                      |
                  v     v                      v
               Android Gradle             Kotlin LSP
                  |     |
              ADB/AVD ./gradlew

                       |
                     MCP
                       |
                       v
             Cursor / Codex / Claude / etc.
```

El núcleo (`mydev`) debería ser un proceso nativo persistente, idealmente Kotlin/Native si las pruebas confirman que la integración necesaria encaja bien.

Kotlin/JVM no es necesario para gestionar filesystem, procesos, sockets, Git, ADB o ejecutar `./gradlew`; esas cosas pueden manejarse desde un proceso nativo. El motivo principal para mantener JVM es el Kotlin LSP oficial, que internamente utiliza infraestructura JetBrains/Kotlin.

---

## 4. División de responsabilidades

### `mydev` core

Debe ser el runtime persistente del entorno de desarrollo.

Responsabilidades potenciales:

- filesystem
- procesos/subprocesos
- Git
- Gradle CLI
- Android CLI
- ADB
- emuladores
- logcat
- estado de dispositivos
- estado de builds/tareas
- conexión con Kotlin LSP
- file watchers
- HTTP/WebSocket para la UI
- servidor MCP
- gestión de procesos persistentes

Importante: **mydev no tiene por qué ser un agent harness**.

Debe poder existir independientemente de la IA.

---

## 5. Agente de IA: harness vs MCP

Conclusión conceptual alcanzada durante el diseño:

- El **LLM** recibe contexto/token input y produce output/token output.
- El **agent harness/runtime** es el proceso externo que mantiene estado, construye contexto, ejecuta tool calls, llama repetidamente al LLM y mantiene el agent loop.
- Un **MCP server** es una interfaz/protocolo para exponer herramientas/recursos al agente.
- Un MCP server puede tener procesos persistentes, estado, threads, event loops, watchers, sockets y child processes. No hace falta ser un plugin de harness para mantener un proceso vivo.
- Lo que un MCP no controla normalmente es el lifecycle interno del agent runtime: prompt assembly, loop, sesiones, eventos internos, etc.
- Los hooks/plugins de algunos agentes permiten intervenir en puntos concretos del lifecycle, pero eso es diferente de implementar el harness completo.

La decisión provisional es:

> **No construir un agent harness propio en la primera versión.**
>
> Hacer que `mydev` sea el runtime del entorno Android y exponer sus capacidades mediante MCP. El harness lo aporta el agente externo (Cursor/Codex/Claude u otro).

Más adelante podría añadirse un adaptador/plugin específico para agentes que expongan hooks/plugins, pero no debe ser dependencia del core.

---

## 6. Qué debería exponer el MCP

La idea no es simplemente envolver comandos shell. El valor diferencial está en **elevar las operaciones a herramientas semánticas y devolver datos estructurados**.

Ejemplos:

```text
project.describe()
project.modules()

code.definition(symbol)
code.references(symbol)
code.symbols(file)
code.callHierarchy(symbol)
code.diagnostics()

build.assemble(variant)
build.test(...)
build.lastResult()
build.errors()

device.list()
device.boot(...)
device.install(...)
device.launch(...)
device.screenshot()
device.uiTree()
device.tapElement(...)
device.logs(...)
device.processes(...)
device.memory(...)
device.crash(...)

emulator.list()
emulator.start(...)
emulator.stop(...)

git.status()
git.diff()
```

Debe seguir existiendo acceso a shell/CLI para casos que no estén modelados.

La idea no es eliminar `bash`/CLI, sino reducir el trabajo de descubrimiento e interpretación del agente cuando una operación frecuente puede exponerse mejor de forma estructurada.

---

## 7. Android CLI oficial de Google

La investigación realizada indica que la Android CLI oficial ya cubre una parte importante del espacio que se pensaba implementar desde cero.

Entre sus capacidades documentadas aparecen, entre otras:

- `android info`
- `android describe`
- `android run`
- `android install`
- gestión de emuladores
- gestión del SDK
- inspección de layout/UI
- captura de pantalla
- resolución de elementos visuales
- búsqueda/fetch de documentación Android
- gestión de skills Android
- algunas integraciones con Android Studio

Por tanto, la estrategia recomendada es **reutilizar Android CLI donde tenga sentido**, en lugar de reimplementar esa funcionalidad.

La CLI no debe asumirse como un simple alias de ADB/SDK manager. Es una herramienta propia de Google que añade lógica encima de las herramientas subyacentes.

### Gradle en Android CLI

`android run` sí participa en el ciclo build -> deploy -> launch y por tanto integra la fase de build.

`android install` trabaja sobre artefactos ya construidos y se centra en el deployment.

El `delta install`/incremental install es distinto de la incrementalidad de Gradle:

```text
Gradle incremental build
    -> evita recompilar trabajo que no cambió

Android CLI delta install
    -> evita transferir al dispositivo trabajo que no cambió
```

La implementación interna exacta del binario de Android CLI no debe asumirse como un simple wrapper: la implementación completa no está publicada en el repositorio de skills; parte de la CLI se distribuye como binario.

---

## 8. Relación con `dsh-android`

Se revisó el proyecto:

`https://github.com/ZSeven-W/dsh-android`

Conclusión: `dsh-android` es un **plugin nativo de DeepSeek Harness**, no un MCP server externo.

Añade varias capas:

```text
dsh-android
├── tools
├── skill
├── AndroidHostController / estado
├── procesos persistentes
├── integración web/UI
└── integración visual/multimodal
```

Las herramientas incluyen operaciones como:

- dispositivos
- boot/emulador
- build/run
- screenshots
- UI tree
- interacción con elementos
- OCR
- logs
- procesos
- backtrace/crashes
- memoria

El plugin mantiene además procesos persistentes (por ejemplo el stream de pantalla) y tiene integración específica con la UI/web del DeepSeek Harness.

### Qué se puede reutilizar conceptualmente

La mayor parte de la lógica Android es separable del harness:

- control ADB
- parsing de resultados
- UI tree
- interacción semántica
- build/install/launch
- logs
- procesos
- memoria
- estado de dispositivo
- loops persistentes

Esto podría migrarse a un runtime propio y exponerlo vía MCP.

### Qué no es portable 1:1

La parte específica de DSH:

- registro interno de plugins
- servicios/event bus del harness
- integración directa con su webserver/UI
- lifecycle interno del agente

No es necesaria para conseguir las capacidades Android principales.

---

## 9. Insight importante: tools vs skills vs hooks vs plugins

Conviene mantener estos conceptos separados:

### Tools

Capacidades ejecutables. Por ejemplo:

```text
android.build_run()
android.ui_tree()
android.logs()
code.references()
```

### Skill

Conocimiento/procedimiento que explica al agente cómo combinar las herramientas y cuál es el workflow correcto.

Ejemplo:

```text
build -> install -> launch -> inspect -> logs -> diagnose -> edit -> rebuild
```

### Hook

Punto de extensión del lifecycle del agent runtime.

Ejemplo conceptual:

```text
preToolUse
postToolUse
sessionStart
stop
```

### Plugin

Paquete de extensibilidad que puede agrupar varias de las anteriores y, dependiendo del host, integrar servicios, UI, estado, etc.

`dsh-android` es principalmente un plugin que aporta tools + skill + runtime/estado + UI integration; no debe modelarse simplemente como un conjunto de hooks.

---

## 10. Kotlin LSP

La idea sigue siendo utilizar el **Kotlin LSP oficial** como proceso separado.

No hay que arrancar Android Studio/IntelliJ como UI.

Arquitectura propuesta:

```text
mydev
  |
  | JSON-RPC / stdio
  v
Kotlin LSP (JVM)
```

El frontend no necesita hablar directamente con LSP. `mydev` puede actuar como cliente/proxy y exponer operaciones más simples:

```text
code.definition()
code.references()
code.symbols()
code.diagnostics()
code.callHierarchy()
```

Esto también permite que la UI humana y el agente utilicen exactamente la misma capa semántica.

---

## 11. Frontend

No usar Electron.

La propuesta inicial es una aplicación web servida por `mydev`:

- TypeScript
- HTML/CSS
- editor ligero (CodeMirror es el candidato natural)
- HTTP/WebSocket para eventos/stream

La razón principal para evitar Monaco es que el objetivo incluye uso desde tablet/móvil; CodeMirror ofrece una arquitectura más modular y apropiada para ese escenario.

La UI puede organizarse conceptualmente así:

```text
+-------------------------------------------------------+
| proyecto | branch | build | run | device             |
+-------------------+-------------------+---------------+
|                   |                   |               |
| project explorer  |     editor        | AI panel      |
|                   |                   |               |
+-------------------+-------------------+---------------+
| Git | Terminal | Logcat | Gradle | Devices | Problems |
+-------------------------------------------------------+
```

---

## 12. Principio de diseño importante

La UI y la IA deberían utilizar **las mismas primitivas internas**.

Por ejemplo:

```text
UI: "Go to definition"
    -> mydev.code.definition()
    -> Kotlin LSP

AI: "find definition of Foo"
    -> MCP code.definition()
    -> mydev.code.definition()
    -> Kotlin LSP
```

Lo mismo para devices, Gradle, Git, logcat, etc.

Esto evita construir una API para humanos y otra completamente distinta para agentes.

---

## 13. Valor diferencial esperado

El proyecto no debería intentar ganar por "tener otro wrapper de ADB".

El valor está en combinar:

```text
Android CLI oficial
        +
Kotlin LSP
        +
Git
        +
Gradle
        +
ADB / emulator / runtime debugging
        +
estado persistente
        +
API semántica
        +
MCP
        +
UI web ligera
```

Especialmente importante es elevar operaciones que un agente general tendría que descubrir mediante shell:

```text
bash / adb / gradle
        -> stdout arbitrario
        -> el LLM interpreta
```

frente a:

```text
mydev MCP
        -> resultado estructurado
        -> menos pasos y menos ambigüedad
```

Ejemplo:

```text
android.tapElement(resourceId="...")
```

es preferible para un agente a obligarlo a descubrir coordenadas y ejecutar:

```text
adb shell input tap X Y
```

Otro ejemplo:

```text
build.errors()
```

puede devolver directamente:

```json
{
  "task": ":app:compileDebugKotlin",
  "errors": [
    {
      "file": "Foo.kt",
      "line": 83,
      "message": "..."
    }
  ]
}
```

---

## 14. Primera versión recomendada

No empezar intentando replicar Android Studio completo.

MVP técnico sugerido:

1. `mydev` core persistente.
2. Filesystem + procesos.
3. Integración con Android CLI oficial.
4. Integración con `./gradlew` para lo que la CLI no cubra.
5. ADB/device manager.
6. Kotlin LSP como proceso sidecar.
7. Git.
8. HTTP/WebSocket.
9. MCP server.
10. UI web mínima.

Después:

- UI tree y automation más avanzada.
- logcat estructurado.
- crash/ANR analysis.
- screenshots/streaming.
- code intelligence más profunda.
- device/emulator orchestration.
- hooks/plugins específicos para agentes cuando aporte valor.

---

## 15. Preguntas abiertas para el agente que continúe el proyecto

### Runtime

- ¿Kotlin/Native es la mejor opción para `mydev` o conviene Rust/Go para el core?
- ¿Qué componentes deben ser procesos persistentes y cuáles pueden ser invocaciones puntuales?
- ¿Cómo gestionar lifecycle y cleanup de ADB/LSP/Gradle/emulators?

### Android CLI

- ¿Qué comandos de Android CLI cubren completamente nuestras necesidades?
- ¿Qué funcionalidades del runtime siguen requiriendo ADB directo?
- ¿Qué limitaciones tiene la CLI actual?
- ¿Cómo detectar de forma robusta la versión/capacidades instaladas?

### MCP

- ¿Qué tool surface es realmente útil y suficientemente estable?
- ¿Qué operaciones deberían ser herramientas atómicas y cuáles workflows de alto nivel?
- ¿Qué estado debe persistir en `mydev`?
- ¿Cuándo devolver datos estructurados y cuándo permitir shell/raw output?

### LSP

- ¿Qué operaciones delegar directamente al Kotlin LSP?
- ¿Cómo mapear `.kt`, `.kts`, Java, XML y Gradle Kotlin DSL?
- ¿Cómo gestionar proyectos Android grandes y consumo de memoria?

### UI

- ¿CodeMirror es suficiente para el editor/navegación?
- ¿Cómo mostrar device streaming desde una tablet?
- ¿Qué UI debe ser propia y qué información puede derivarse directamente de las APIs del core?

### Integración con agentes

- ¿Conviene distribuir solamente un MCP server?
- ¿Conviene además ofrecer Skills?
- ¿En qué casos merece la pena crear adaptadores/plugins/hooks para cada agente?

---

## 16. Fuentes y proyectos de referencia

- Android CLI / skills: https://github.com/android/skills
- Android CLI docs: https://developer.android.com/tools/agents/android-cli
- dsh-android: https://github.com/ZSeven-W/dsh-android
- DeepSeek Harness: https://github.com/deepseek-ai/deepseek-harness
- Kotlin LSP: https://github.com/Kotlin/kotlin-lsp
- Kotlin LSP docs: https://kotlinlang.org/docs/kotlin-lsp.html
- Language Server Protocol: https://microsoft.github.io/language-server-protocol/
- CodeMirror: https://codemirror.net/
- Gradle Tooling API: https://docs.gradle.org/current/userguide/tooling_api.html

---

## 17. Resumen ejecutivo

La idea final es construir un **runtime ligero de desarrollo Android orientado a agentes**, no un clon completo de Android Studio.

`mydev` debe ser agnóstico de la IA y proporcionar una API única para humanos y agentes. Debe reutilizar Android CLI donde exista funcionalidad oficial y añadir una capa propia para las capacidades que faltan: Kotlin LSP, Git, debugging/runtime Android, estado persistente y una API MCP.

La IA externa aporta el agent harness y el agent loop. `mydev` aporta el entorno real sobre el que ese agente trabaja.

La arquitectura objetivo queda resumida en:

```text
                    Cursor / Codex / Claude / otro agente
                                   |
                                  MCP
                                   |
                            +--------------+
                            |    mydev     |
                            +--------------+
                            | core runtime |
                            +--------------+
                              |  |  |  |  |
                              |  |  |  |  +-- Git
                              |  |  |  +----- Kotlin LSP
                              |  |  +-------- Gradle
                              |  +----------- Android CLI / ADB
                              +-------------- Emulator / runtime
                                   |
                              HTTP/WebSocket
                                   |
                                Browser
```

La hipótesis principal a validar es que una capa semántica Android estable sobre estas herramientas permite a agentes generales trabajar de forma más eficiente y fiable que usando exclusivamente shell + CLI, sin necesidad de crear un harness de IA propio.
