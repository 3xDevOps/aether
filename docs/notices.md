# Open source notices

Aether, including the Android app (package `io.aether.android`) and the
dashboard it shows, is licensed under the GNU General Public License version
3. The full text is [LICENSE](../LICENSE) in this repository, which the app's
first screen links to. The source for the exact build a release ships is the
tag that release was cut from.

These notices cover the Android APK, dashboard assets, and the separately
distributed standard-environment and browser-companion images. The APK does not
contain the server or these container images. Each bundled dependency keeps its
own license; Aether's GPL-3.0 license does not replace those terms.

## In the APK

The Android app links two Google libraries, both under the Apache License 2.0:

- `androidx.core:core`
- `androidx.activity:activity`

Their transitive dependencies - the Kotlin standard library, kotlinx
coroutines, the rest of androidx - are Apache-2.0 as well. The exact versions
and their checksums are in
[`android/gradle/verification-metadata.xml`](../android/gradle/verification-metadata.xml);
the direct ones are in
[`android/gradle/libs.versions.toml`](../android/gradle/libs.versions.toml).
A copy of the Apache-2.0 text ships inside the APK at
`META-INF/androidx/annotation/annotation/LICENSE.txt`.

## In the dashboard bundle

The dashboard ships two fonts, which the Play listing's feature graphic is
drawn with as well. Both are under the SIL Open Font License 1.1: VT323
([`web/public/fonts/LICENSE-vt323.txt`](../web/public/fonts/LICENSE-vt323.txt))
and `JetBrainsMono NFM`, a Nerd Fonts Mono patch of JetBrains Mono
([`web/public/fonts/LICENSE-jetbrains-mono-nfm.txt`](../web/public/fonts/LICENSE-jetbrains-mono-nfm.txt)).

The terminal uses [xterm.js](https://github.com/xtermjs/xterm.js), including its
fit, search and web-links addons, under the
[MIT license](https://github.com/xtermjs/xterm.js/blob/6.0.0/LICENSE).
Dashboard dependency versions and resolved integrity records are in
[`web/package.json`](../web/package.json) and [`web/bun.lock`](../web/bun.lock).

## In the standard environment image

Git is installed from Ubuntu 24.04's distribution packages, not a separate
upstream source build. It is covered by Git's GPL version 2 terms and the
per-file notices recorded in `/usr/share/doc/git/copyright`. The exact
installed version is recorded in the image's dpkg database; the
[Ubuntu Git package page](https://packages.ubuntu.com/noble/git) links its
copyright record and corresponding source package, including Ubuntu's
packaging and patches. Source packages are obtained from the Ubuntu archive;
the image does not retain a separate upstream Git tarball or build recipe.

The other installed toolchains and Ubuntu packages are selected by
[`images/standard/Dockerfile`](../images/standard/Dockerfile). Ubuntu packages
carry their copyright and license records under `/usr/share/doc/<package>/`;
their source-package records are available through the
[Ubuntu package archive](https://packages.ubuntu.com/noble/).
This notice is not a complete transitive-license audit or a source-distribution
claim for every toolchain in that image.

## In the headless browser companion image

The separately distributed `aether-browser` image includes:

| Component | Version/source | License/notice source |
| --- | --- | --- |
| Node.js | 24.20.0, Debian Bookworm slim base pinned by digest in [`images/browser/Dockerfile`](../images/browser/Dockerfile) | [Node.js license and third-party notices](https://github.com/nodejs/node/blob/v24.20.0/LICENSE) |
| Playwright and playwright-core | 1.63.0, pinned with integrity hashes in [`images/browser/package-lock.json`](../images/browser/package-lock.json) | [Apache-2.0](https://github.com/microsoft/playwright/blob/v1.63.0/LICENSE) and package notices |
| Chromium / Chrome for Testing | 153.0.8010.12, Playwright revision 1243, selected by the pinned [browser manifest](https://github.com/microsoft/playwright/blob/v1.63.0/packages/playwright-core/browsers.json) | [Chromium BSD-style license](https://github.com/chromium/chromium/blob/153.0.8010.12/LICENSE), plus the browser distribution's third-party notices; Chromium is not covered solely by Playwright's license |
| FFmpeg | Playwright revision 1011, installed alongside Chromium by Playwright | [FFmpeg license terms](https://ffmpeg.org/legal.html); the [Linux distribution archive](https://cdn.playwright.dev/dbazure/download/playwright/builds/ffmpeg/1011/ffmpeg-linux.zip) carries `COPYING.LGPLv2.1`, retained under `/opt/playwright/ffmpeg-1011/` |
| xterm.js | 6.0.0, pinned in the same package lock | [MIT](https://github.com/xtermjs/xterm.js/blob/6.0.0/LICENSE) |
| JetBrains Mono NFM regular and bold | The same WOFF2 assets used by the dashboard, copied from `web/public/fonts/` | [SIL OFL 1.1](../web/public/fonts/LICENSE-jetbrains-mono-nfm.txt), copied into `/opt/aether-browser/fonts/` |

Playwright's pinned
[Debian dependency list](https://github.com/microsoft/playwright/blob/v1.63.0/packages/playwright-core/src/server/registry/nativeDeps.ts)
also selects browser libraries and fallback fonts: `fonts-noto-color-emoji`,
`fonts-unifont`, `xfonts-scalable`, `fonts-liberation`, `fonts-ipafont-gothic`,
`fonts-wqy-zenhei`, `fonts-tlwg-loma-otf`, and `fonts-freefont-ttf`.
These are Debian packages, not copies relicensed under Playwright's Apache
license. Exact installed versions are recorded in the image's dpkg database;
the font and library terms are in `/usr/share/doc/<package>/copyright`.
The [Debian Bookworm package archive](https://packages.debian.org/bookworm/)
links each package's source and copyright records. This includes font-specific
terms and exceptions; do not assume every fallback font has the same license.

The image build keeps npm package license files and distribution package
copyright records. Consult those records and Chromium's third-party notices
when redistributing an image. This inventory does not assert that every
transitive component has been independently license-audited.

## Not distributed by the app

The dashboard's JavaScript dependencies and the server's Go modules run on
the member's own server, not on the phone: the APK carries none of them.
They are declared with their versions in
[`web/package.json`](../web/package.json) and [`go.mod`](../go.mod), each
under its own licence.
