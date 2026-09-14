# Open source notices

Aether, including the Android app (package `io.aether.android`) and the
dashboard it shows, is licensed under the GNU General Public License version
3. The full text is [LICENSE](../LICENSE) in this repository, which the app's
first screen links to. The source for the exact build a release ships is the
tag that release was cut from.

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

The dashboard ships two fonts, which the Play listing's feature graphic is
drawn with as well. Both are under the SIL Open Font License 1.1: VT323
([`web/public/fonts/LICENSE-vt323.txt`](../web/public/fonts/LICENSE-vt323.txt))
and JetBrains Mono Nerd Font
([`web/public/fonts/LICENSE-jetbrains-mono-nfm.txt`](../web/public/fonts/LICENSE-jetbrains-mono-nfm.txt)).
