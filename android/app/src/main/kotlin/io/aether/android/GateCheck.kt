package io.aether.android

/**
 * The fields of the Edge gateway's `update.check` the update gate reads:
 * whether the server refuses this app until it updates, the versions on
 * both sides, the newest release, empty when the release lookup failed, and
 * the release the gateway would install, empty from a gateway before it.
 */
data class GateCheck(
    val required: Boolean,
    val clientVersion: String,
    val serverVersion: String,
    val minClientVersion: String,
    val latest: String,
    val targetVersion: String,
)

data class Gate(
    val clientVersion: String,
    val serverVersion: String,
    val minClientVersion: String,
    val tag: String,
)

private val HANDSHAKE_ORIGIN = Regex("^(http://127\\.0\\.0\\.1:\\d{1,5})/\\?token=([A-Za-z0-9_-]+)$")

data class GatewayCall(val url: String, val token: String)

fun gatewayCall(handshakeUrl: String, verb: String): GatewayCall? {
    val (origin, token) = HANDSHAKE_ORIGIN.matchEntire(handshakeUrl)?.destructured ?: return null
    return GatewayCall("$origin/local/v1/$verb", token)
}

// The body is json.Encoder output of one object with the cli check nested
// under "cli"; org.json is a stub in the unit tests' android.jar. Every key
// read here is unique in that object, and a value with a backslash is not
// one this gate shows.
private fun stringField(json: String, name: String): String? =
    Regex("\"$name\"\\s*:\\s*\"([^\"\\\\]*)\"").find(json)?.groupValues?.get(1)

private fun boolField(json: String, name: String): Boolean? =
    Regex("\"$name\"\\s*:\\s*(true|false)").find(json)?.groupValues?.get(1)?.let { it == "true" }

/**
 * The check in an `update.check` response [body], or null when it is not
 * one. A gateway from before the update gate omits `client_update_required`,
 * which reads as not required.
 */
fun parseGateCheck(body: String): GateCheck? {
    val json = body.trim()
    if (!json.startsWith("{") || !json.endsWith("}")) return null
    val clientVersion = stringField(json, "version") ?: return null
    return GateCheck(
        required = boolField(json, "client_update_required") ?: false,
        clientVersion = clientVersion,
        serverVersion = stringField(json, "server_version") ?: "",
        minClientVersion = stringField(json, "min_client_version") ?: "",
        latest = stringField(json, "latest") ?: "",
        targetVersion = stringField(json, "target_version") ?: "",
    )
}

/**
 * The gate for [check], or null to let the dashboard through. The server
 * decides; this only refuses to gate on a version it cannot build a URL
 * from, so a dev build or a malformed answer never locks anyone out. What
 * to install is the gateway's target release, else the newest release,
 * else the oldest one the server accepts. The newest release can trail
 * that oldest one - GitHub's latest skips prereleases and is cached - and
 * an older APK is refused as a downgrade or leaves the gate up, so no tag
 * below it is offered.
 */
fun gateFor(check: GateCheck?): Gate? {
    if (check == null || !check.required) return null
    val min = check.minClientVersion
    if (!isReleaseTag(check.clientVersion) || !isReleaseTag(min)) return null
    val tag =
        listOf(check.targetVersion, check.latest).firstOrNull { isReleaseTag(it) && compareReleaseTags(it, min) >= 0 }
            ?: min
    return Gate(check.clientVersion, check.serverVersion, min, tag)
}

// Semantic version precedence of two tags isReleaseTag accepts.
private fun compareReleaseTags(a: String, b: String): Int {
    val (aCore, aPre) = splitTag(a)
    val (bCore, bPre) = splitTag(b)
    for (i in 0 until 3) {
        val c = aCore[i].toBigInteger().compareTo(bCore[i].toBigInteger())
        if (c != 0) return c
    }
    // A release outranks its own prereleases.
    if (aPre == null || bPre == null) return (if (aPre == null) 1 else 0) - (if (bPre == null) 1 else 0)
    for ((x, y) in aPre.zip(bPre)) {
        val xn = x.toBigIntegerOrNull()
        val yn = y.toBigIntegerOrNull()
        val c =
            when {
                xn != null && yn != null -> xn.compareTo(yn)
                xn != null -> -1
                yn != null -> 1
                else -> x.compareTo(y)
            }
        if (c != 0) return c
    }
    return aPre.size.compareTo(bPre.size)
}

private fun splitTag(tag: String): Pair<List<String>, List<String>?> {
    val parts = tag.removePrefix("v").split('-', limit = 2)
    return parts[0].split('.') to parts.getOrNull(1)?.split('.')
}

fun gateDetails(gate: Gate, targets: List<UpdateTarget>): String =
    buildString {
        append("This Aether app (${gate.clientVersion}) is too old for the server ")
        append("(${gate.serverVersion} needs ${gate.minClientVersion} or newer).\n")
        append("Install ${gate.tag}:\n")
        for (target in targets) append(target.url).append('\n')
    }
