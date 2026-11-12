# Editors

The JSON Schema of the format gives completion and checking as you type. The schema describes the YAML form only. For a
HOCON proxyfile, see [Editor support](#editor-support) below. Put this line first in a YAML proxyfile, for editors that
use the YAML language server, such as VS Code with the YAML extension:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/kitecorp/proxyfile/v2.0.0/schema/proxyfile.schema.json
```

In a JetBrains IDE, open Settings, Languages & Frameworks, Schemas and DTDs, JSON Schema Mappings. Add a mapping with the
schema URL above and a file pattern that matches your YAML proxyfiles, such as `proxyfile.yaml`.

`proxyfile schema` prints the same schema, and each release carries it as `proxyfile.schema.json`.

## Editor support

IntelliJ IDEA and the other JetBrains IDEs highlight HOCON through the HOCON plugin, `org.jetbrains.plugins.hocon`,
which reads `.conf` files. A `Proxyfile` has no extension, so add the file name pattern `Proxyfile` to the HOCON file
type under Settings, Editor, File Types (`proxyfile.conf` needs no pattern).

VS Code highlights it through the HOCON extension `sabieber.HOCON`, which reads `.conf` and `.hocon` files as the
language `hocon`; `"files.associations": {"Proxyfile": "hocon"}` in its settings adds the name.

GitHub colours a file by Linguist, which knows HOCON but maps it only to `.hocon` and two scalafmt file names, so a
repository's `.gitattributes` gets the line `Proxyfile linguist-language=HOCON`, and
`*.conf linguist-language=HOCON` where its `.conf` files are proxyfiles, as in:

```
Proxyfile linguist-language=HOCON
*.conf linguist-language=HOCON
```

This repository's `.gitattributes` holds both.
