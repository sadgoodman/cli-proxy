# cli-proxy

[![CI](https://github.com/sadgoodman/cli-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/sadgoodman/cli-proxy/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/sadgoodman/cli-proxy)](https://github.com/sadgoodman/cli-proxy/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.25-00ADD8?logo=go&logoColor=white)](go.mod)

[English](README.md) · [Русский](README.ru.md) · [中文](README.zh.md) · **Español**

Un proxy CLI ligero para observar y reescribir tráfico HTTP y HTTPS, con una
interfaz de terminal que se maneja con teclado y ratón. Un único binario
estático, **sin dependencias externas** — solo la biblioteca estándar.

```
╭─ cli-proxy ───────────────────────────────────── 0.0.0.0:8080 · MITM · 128 flows ─╮
│  Flows   Filters   Rules   Cert   Log   Help                                       │
├────────────────────────────────────────────────────────────────────────────────────┤
│╭─ flows · 128 shown / 128 captured ─────────┬───────────────────┬────────┬────────╮ │
││    # │ Time     │ Method │ Host            │ Path              │ Status │   Size │ │
│├──────┼──────────┼────────┼─────────────────┼───────────────────┼────────┼────────┤ │
││    1 │ 12:04:11 │ GET    │ api.example.com │ /v1/users         │    200 │   1.2K │ │
││    2 │ 12:04:11 │ POST   │ api.example.com │ /v1/orders        │    201 │    84B │ │
││    3 │ 12:04:12 │ GET    │ cdn.example.net │ /logo.png         │    404 │    0B  │ │
│╰──────┴──────────┴────────┴─────────────────┴───────────────────┴────────┴────────╯ │
╰──[↵]open──[f]filter──[b]brk-req──[m]mock──[M]redir──[i]break──[s]save──[q]quit──────╯
```

## Aspectos destacados

- **Intercepción en tiempo real** de HTTP y HTTPS. Una CA raíz incluida genera
  un certificado por host sobre la marcha, y HTTP/2 funciona sobre ALPN.
  `-tunnel` mantiene el CONNECT opaco cuando solo quieres ver hacia dónde va el
  tráfico.
- **Puntos de interrupción** que pausan una petición o una respuesta y abren el
  mensaje HTTP sin procesar para editarlo. El cuerpo se toma tal cual y
  `Content-Length` se recalcula, así que no se trunca nada.
- **Mocks y reescrituras** mediante un DSL de reglas compacto: servir un archivo
  local, obtener la respuesta desde otra dirección, devolver un 302 real,
  sobrescribir el estado o el cuerpo, reescribir cabeceras, agregar latencia,
  bloquear. Una regla se puede crear a partir de un flujo capturado con una sola
  tecla.
- **Búsqueda y filtros** por URL, método, estado, host, payload y etiquetas, con
  negación, `AND`, `OR` y paréntesis. Los filtros guardados siguen activos
  mientras te mueves entre pestañas y sobreviven a un reinicio.
- **Payloads legibles.** El JSON se reindenta y se colorea, las líneas largas se
  ajustan en los límites de palabra en lugar de cortarse, y el proxy deja de
  pedir a los servidores codificaciones que no puede decodificar.
- **Paneles de petición y respuesta** con pestañas `headers` / `body` / `raw`,
  uno al lado del otro, y cada uno plegable — tanto en la lista de flujos como
  en la vista de detalle completa.
- **Usa el proxy con cualquier dispositivo.** Una URL corta
  (`http://cli.proxy/ssl`), un certificado raíz instalable, un perfil de iOS e
  instrucciones para cada plataforma. El certificado se puede confiar con una
  sola tecla, y el proxy del sistema se puede apuntar aquí y se restaura al
  salir, incluso después de un cierre forzado.
- **Una interfaz minimalista pero completa.** Tablas con esquinas redondeadas,
  seis pestañas controladas con las teclas de flecha, las teclas numéricas o el
  ratón, con resaltado al pasar el cursor en todas partes.

## Instalación

Descarga un archivo precompilado desde la [última versión][rel], o compílalo
desde el código fuente con Go 1.25+.

| Plataforma | Artefacto |
|---|---|
| Linux x86-64 | `cli-proxy_<version>_linux_amd64.tar.gz` |
| Linux arm64 | `cli-proxy_<version>_linux_arm64.tar.gz` |
| macOS Apple Silicon | `cli-proxy_<version>_darwin_arm64.tar.gz` |
| macOS Intel | `cli-proxy_<version>_darwin_amd64.tar.gz` |
| Windows x86-64 | `cli-proxy_<version>_windows_amd64.zip` |

`checksums.txt` en la misma versión contiene el SHA-256 de cada archivo.

```sh
tar -xzf cli-proxy_<version>_darwin_arm64.tar.gz

./cli-proxy                  # terminal UI on 0.0.0.0:8080
./cli-proxy -install-cert    # trust the root certificate (no password on macOS)
./cli-proxy -system-proxy    # route this machine through it, undone on exit
```

Compilar desde el código fuente:

```sh
git clone https://github.com/sadgoodman/cli-proxy.git
cd cli-proxy && make build && ./cli-proxy
```

## Documentación

La referencia completa — todas las opciones, el DSL de reglas, la sintaxis de
filtros, los atajos de teclado, el manejo del certificado y del proxy del
sistema, y la estructura del proyecto — está disponible en inglés en
**[docs/guide.md](docs/guide.md)** y en ruso en
**[docs/guide.ru.md](docs/guide.ru.md)**.

## Estado

`go test ./...` cubre la intercepción HTTP y HTTPS de extremo a extremo, cada
acción de regla, la edición con puntos de interrupción, el modo túnel, la
instalación del certificado, los filtros guardados, las pestañas de los paneles,
el formato y el ajuste de JSON, el análisis de teclado y ratón, y la geometría
de las tablas. La CI ejecuta la suite en Linux, macOS y Windows, además de una
pasada con `-race` y una verificación de compilación cruzada.

Las contribuciones son bienvenidas — consulta
[CONTRIBUTING.md](CONTRIBUTING.md).

## Licencia

[MIT](LICENSE)

[rel]: https://github.com/sadgoodman/cli-proxy/releases/latest
