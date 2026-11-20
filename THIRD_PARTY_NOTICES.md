# Third-party notices for Oddward

Oddward's original code is licensed GPL-3.0-or-later. Third-party components
retain their own licenses, copyright notices and patent grants. The complete
texts listed below are distributed with the application; these summaries and
source links do not replace them.

In this repository the texts are under `licenses/`. Standalone launcher builds
put this notice and the texts in `licenses/` next to the executable. Installed
and update-package builds put them in the versioned application's `licenses/`
directory. Paths in the tables below are relative to that license directory.
`Oddward.LICENSE.txt` and `loginui/LICENSES.md` are also copied there at build
time from the repository's `LICENSE` and helper's attribution notice.

## Go executable components

The Windows/amd64 launcher and stub use Go 1.27.3 and the exact module versions
in `go.mod`. The following list was checked against their Go package dependency
closure with the `mygo_noinspector` release tag.

| Component and pinned version | License | Included text |
| --- | --- | --- |
| Go runtime and standard library, `go1.27.3` | BSD-3-Clause with additional patent grant | `go/LICENSE.txt`, `go/PATENTS.txt` |
| `github.com/egoist/mygo` `v0.10.0` | MIT | `mygo/LICENSE.txt` |
| MyGo `internal/shader` and `internal/shaderir`, same release | Apache-2.0 | `mygo/shader-LICENSE.txt` |
| `github.com/ebitengine/purego` `v0.11.1` | Apache-2.0 | `purego/LICENSE.txt` |
| `github.com/go-text/typesetting` `v0.3.5` | Unlicense OR BSD-3-Clause | `typesetting/LICENSE.txt` (both alternatives) |
| `golang.org/x/image` `v0.46.0` | BSD-3-Clause with additional patent grant | `golang-x-image/LICENSE.txt`, `golang-x-image/PATENTS.txt` |
| `golang.org/x/net` `v0.59.0` | BSD-3-Clause with additional patent grant | `golang-x-net/LICENSE.txt`, `golang-x-net/PATENTS.txt` |
| `golang.org/x/sys` `v0.49.0` | BSD-3-Clause with additional patent grant | `golang-x-sys/LICENSE.txt`, `golang-x-sys/PATENTS.txt` |
| `golang.org/x/text` `v0.43.0` | BSD-3-Clause with additional patent grant | `golang-x-text/LICENSE.txt`, `golang-x-text/PATENTS.txt` |
| `modernc.org/sqlite` `v1.62.0` (pure-Go SQLite driver; no SQLite DLL or C toolchain is required) | BSD-3-Clause | `modernc-sqlite/LICENSE.txt`, `modernc-sqlite/THIRD-PARTY.md` |
| SQLite amalgamation generated into `modernc.org/sqlite` `v1.62.0` (SQLite 3.54.1) | Public domain | `modernc-sqlite/SQLITE.txt` |
| `modernc.org/libc` `v1.80.1` | BSD-3-Clause | `modernc-libc/LICENSE.txt`, `modernc-libc/THIRD-PARTY.md` |
| `modernc.org/mathutil` `v1.7.1` | BSD-3-Clause | `modernc-mathutil/LICENSE.txt` |
| `modernc.org/memory` `v1.12.1` | BSD-3-Clause | `modernc-memory/LICENSE.txt`, `modernc-memory/GO.txt`, `modernc-memory/MMAP-GO.txt` |
| `github.com/dustin/go-humanize` `v1.1.0` | MIT | `go-humanize/LICENSE.txt` |
| `github.com/google/uuid` `v1.6.0` | BSD-3-Clause | `google-uuid/LICENSE.txt` |
| `github.com/mattn/go-isatty` `v0.0.24` | MIT | `go-isatty/LICENSE.txt` |
| `github.com/ncruces/go-strftime` `v1.1.0` | MIT | `go-strftime/LICENSE.txt` |
| `github.com/remyoudompheng/bigfft` `v0.0.0-20230129092748-24d4a6f8daec` | BSD-3-Clause | `bigfft/LICENSE.txt` |
| Public Suffix List data embedded by `golang.org/x/net/publicsuffix`, commit `d6c92f1bbb7433e5db7b8405c25d4035fb8ff376` | MPL-2.0 | `publicsuffix/LICENSE.txt` |

The SQLite driver is linked statically: the amalgamation is generated into Go
by `modernc.org/sqlite`, so no `sqlite3.dll` is installed or loaded at runtime.
`modernc.org/libc` is the C-library shim that driver needs; on Windows it also
uses the bundled `go-strftime` implementation of `strftime`. Its third-party
notice covers the Go-derived portions of those packages.

MyGo's shader compiler and intermediate representation retain the source
notices `Copyright 2020 The Ebiten Authors`, `Copyright 2022 The Ebiten Authors`
and `Copyright 2023 The Ebitengine Authors`. MyGo marks its shader changes in
the upstream source files. Purego's Windows implementation retains
`SPDX-FileCopyrightText: 2022 The Ebitengine Authors`.

The typesetting packages used here are `language`, `internal/unicodedata` and
`segmenter`; its separately licensed HarfBuzz package is not in this Windows
build's package dependency closure. Unused MyGo plugins and module-cache-only
development tools are likewise not listed as shipped components.

The Public Suffix List's exact source data is available at
<https://raw.githubusercontent.com/publicsuffix/list/d6c92f1bbb7433e5db7b8405c25d4035fb8ff376/public_suffix_list.dat>.
The generated representation and generation program are in
<https://github.com/golang/net/tree/v0.59.0/publicsuffix>.

## Official game artwork

`internal/launcher/assets/stamina.webp` and
`internal/launcher/assets/city-stamina.webp` are the in-game resource icons
for 本性像素 and 都市活力. They remain the property of their rights holder
and are included only to label those game resources; they are not covered by
Oddward's GPL-3.0-or-later license.

## Community wiki metadata and pinyin index

`internal/bwiki` reads public arc name, rarity and icon fields from the
Bilibili 异环 BWIKI at runtime. The wiki's prose is not bundled or cached; the
cache stores only those three display fields and their icons. BWIKI content is
contributed by its editors and remains subject to the wiki's terms at
<https://wiki.biligame.com/yh/首页>; the icons remain game artwork under the
notice above.

`internal/pinyin/pinyin.bin` is the Unihan `kMandarin` table used by MyGo,
licensed under the Unicode License V3. The complete text is in
`licenses/unicode/LICENSE.txt`; the upstream notice is at
<https://www.unicode.org/license.txt>.

## Self-contained login window

The original Oddward login helper uses .NET SDK `10.0.303` to publish the
`win-x64` runtime packs below at version `10.0.11`. The SDK itself is a build
tool; the runtime and Windows Desktop Framework are carried in the embedded
helper payload. The runtime-pack license files were copied from the exact
packages restored from NuGet.org, not from a moving repository branch.

| Component | Included text |
| --- | --- |
| `Microsoft.NETCore.App.Runtime.win-x64` `10.0.11` | `dotnet/microsoft.netcore.app.runtime.win-x64/LICENSE.TXT`, `dotnet/microsoft.netcore.app.runtime.win-x64/THIRD-PARTY-NOTICES.TXT` |
| `Microsoft.WindowsDesktop.App.Runtime.win-x64` `10.0.11` | `dotnet/microsoft.windowsdesktop.app.runtime.win-x64/LICENSE.txt` |
| WPF third-party notices for that Windows Desktop build | `dotnet/microsoft.windowsdesktop.app.runtime.win-x64/wpf-THIRD-PARTY-NOTICES.txt` |
| Windows Forms third-party notices for that Windows Desktop build | `dotnet/microsoft.windowsdesktop.app.runtime.win-x64/winforms-THIRD-PARTY-NOTICES.txt` |

The two runtime packs' NuGet metadata identifies repository
`https://github.com/dotnet/dotnet`, commit
`e2f47b0110ed922f21a1522da67279133ce28f32`. Their primary licenses are MIT,
Copyright (c) .NET Foundation and Contributors. The Windows Desktop package
contains `LICENSE` but no separate third-party-notices file, so its WPF and
Windows Forms notices are included verbatim from that exact repository commit:

- <https://raw.githubusercontent.com/dotnet/dotnet/e2f47b0110ed922f21a1522da67279133ce28f32/src/wpf/THIRD-PARTY-NOTICES.TXT>
- <https://raw.githubusercontent.com/dotnet/dotnet/e2f47b0110ed922f21a1522da67279133ce28f32/src/winforms/THIRD-PARTY-NOTICES.TXT>

Upstream aggregate notices are retained in full, including their original
links and descriptions; an entry in those aggregate notices does not by itself
mean that every listed component is used by Oddward. The helper's own
`loginui/LICENSES.md` preserves original-code attribution and historical
upstream attribution separately.

## Icon resource build tool

[`github.com/akavel/rsrc` `v0.10.2`](https://github.com/akavel/rsrc/tree/v0.10.2)
is used only by `scripts/update-icons.ps1` to encode Oddward's original icon
as Windows COFF resources. It is licensed under
[MIT, Copyright 2013–2017 The rsrc Authors](https://github.com/akavel/rsrc/blob/v0.10.2/LICENSE.txt).
The generator's executable and implementation are not included in the application;
ordinary builds link the checked-in icon data without running the generator.

## Provenance and updates

`SOURCES.json` records each included upstream text's component, exact version,
source URL, original path, original-byte SHA-256 and a SHA-256 normalized only
for UTF-8 BOM and CRLF differences. Texts are copied without abridgement or
rewriting. Recheck this inventory, actual dependency closure and source hashes
when changing Go, module or .NET runtime versions. The license-copy script
checks the normalized hashes before preparing the distribution directory.

Proprietary game SDKs, game binaries, logos, videos, fonts and other game media
are not distributed by this license bundle. Their owners' terms remain
applicable; Oddward's license grants no rights to redistribute them.
