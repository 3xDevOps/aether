plugins {
    alias(libs.plugins.android.application)
}

// The release tag and a code that rises with every release, passed in by
// `make android`. A bare Gradle invocation - Android Studio, a contributor
// checking a change - needs neither.
val aetherVersionName = providers.gradleProperty("aetherVersionName").getOrElse("dev")
val aetherVersionCode = providers.gradleProperty("aetherVersionCode").getOrElse("1").toInt()

// Release signing comes from the environment; the four names are the release
// workflow's secrets. Reading them at configuration time is the only option -
// AGP's signing properties are eager, not lazy.
val signingVariables =
    listOf(
        "ANDROID_KEYSTORE_FILE",
        "ANDROID_KEYSTORE_PASSWORD",
        "ANDROID_KEY_ALIAS",
        "ANDROID_KEY_PASSWORD",
    )

fun signingVariable(name: String): String? =
    providers.environmentVariable(name).orNull?.takeIf { it.isNotBlank() }

val signingMissing = signingVariables.filter { signingVariable(it) == null }

// All four or none. None at all is the unsigned build a pull request makes; a
// partial set is a mistake, and finding out from an unsigned artifact days
// later is worse than stopping here.
if (signingMissing.isNotEmpty() && signingMissing.size < signingVariables.size) {
    throw GradleException(
        "Android release signing needs all of ${signingVariables.joinToString(", ")}. " +
            "Not set: ${signingMissing.joinToString(", ")}.",
    )
}

val keystore = signingVariable("ANDROID_KEYSTORE_FILE")?.let(::file)
if (keystore != null && !keystore.isFile) {
    throw GradleException("ANDROID_KEYSTORE_FILE does not name a file: $keystore")
}

android {
    namespace = "io.aether.android"
    // The only platform and build-tools revision the pinned container carries,
    // so the build downloads no SDK package.
    compileSdk = 36
    buildToolsVersion = "36.0.0"

    defaultConfig {
        applicationId = "io.aether.android"
        // 26 is where adaptive launcher icons and the modern WebView start.
        minSdk = 26
        targetSdk = 36
        versionCode = aetherVersionCode
        versionName = aetherVersionName
    }

    signingConfigs {
        if (keystore != null) {
            create("release") {
                storeFile = keystore
                storePassword = signingVariable("ANDROID_KEYSTORE_PASSWORD")
                keyAlias = signingVariable("ANDROID_KEY_ALIAS")
                keyPassword = signingVariable("ANDROID_KEY_PASSWORD")
                // No device that can install a minSdk 26 APK needs the JAR
                // signature; v2 and v3 cover all of them.
                enableV1Signing = false
                enableV2Signing = true
                enableV3Signing = true
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            // With the keystore variables absent this is null and the APK
            // comes out unsigned rather than signed with the debug key.
            // `make android` names the file for what it is and the release
            // workflow refuses to publish it.
            signingConfig = signingConfigs.findByName("release")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    implementation(libs.androidx.activity)
    implementation(libs.androidx.core)
    testImplementation(libs.junit)
}
