# Third-party notices — Oddward login window helper

`compat/loginui` is original Oddward code, licensed GPL-3.0-or-later (see the
repository `LICENSE`). It is built with the .NET 10 SDK and WPF, which are
distributed by the .NET Foundation under the MIT license. The self-contained
publish output therefore carries the .NET runtime and Windows Desktop
Framework assemblies. Their licenses and third-party notices come from the
exact runtime packages and fixed source revisions documented in the project's
THIRD_PARTY_NOTICES.md and included with each distribution.
Self-contained publication also preserves the restored runtime packs' original
license and third-party notice files under `licenses/<NuGetPackageId>/` in the
helper output, including when that output is embedded in the launcher.

`Presentation/LoginSurface.cs` and `Presentation/LoginVisuals.cs` are original
Oddward GPL-3.0-or-later sources, Copyright 2026 Oddward contributors. Their
`Nte.LoginHost.Presentation` namespace is retained for source compatibility;
the helper does not build or load the former upstream LoginHost.

## Historical upstream attribution

The removed compatibility host referenced Hi3Helper.Plugin.NTE `v0.9.4`,
commit `ebe96d758f3b9bf63a68bb2a22bcae4d3df5765f`, under the
[MIT license](https://github.com/maxinglo/Hi3Helper.Plugin.NTE/blob/ebe96d758f3b9bf63a68bb2a22bcae4d3df5765f/LICENSE).
Its Collapse XAML provenance named commit
`4767fb3f7e81b09a9dc159f55626a74d27e13f42`, also MIT, in the
[upstream manifest](https://github.com/maxinglo/Hi3Helper.Plugin.NTE/blob/ebe96d758f3b9bf63a68bb2a22bcae4d3df5765f/src/Hi3Helper.Plugin.NTE.LoginHost/ThirdParty/Collapse/UPSTREAM.json).
These notices are retained for attribution. No upstream LoginHost sources,
WinUI templates, branding images, fonts or binaries are included in this helper.
Third-party branding and proprietary game/SDK files retain their original
terms; this helper's GPL license does not authorize their redistribution.

The upstream MIT notice is:

> Copyright (c) 2025 Collapse Launcher
>
> Permission is hereby granted, free of charge, to any person obtaining a copy
> of this software and associated documentation files (the "Software"), to deal
> in the Software without restriction, including without limitation the rights
> to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
> copies of the Software, and to permit persons to whom the Software is
> furnished to do so, subject to the following conditions:
>
> The above copyright notice and this permission notice shall be included in all
> copies or substantial portions of the Software.
>
> THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
> IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
> FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
> AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
> LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
> OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
> SOFTWARE.
