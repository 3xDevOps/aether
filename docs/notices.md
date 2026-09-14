# Open source notices

Aether, including the Android app (package `io.aether.android`) and the
dashboard it shows, is licensed under the GNU General Public License version
3. The full text is [LICENSE](../LICENSE) in this repository, which the app's
first screen links to. The source for the exact build a release ships is the
tag that release was cut from.

What follows is what Aether's own artifacts carry: the code inside the APK,
and the fonts in the dashboard bundle the server serves. The rest of what a
running Aether uses is on the member's own server and is not distributed by
the app; it is listed at the end.

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

## Not distributed by the app

The dashboard's JavaScript dependencies and the server's Go modules run on
the member's own server, not on the phone: the APK carries none of them.
They are declared with their versions in
[`web/package.json`](../web/package.json) and [`go.mod`](../go.mod), each
under its own licence.
