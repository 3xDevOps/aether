package io.aether.android

/**
 * The fields of the Edge gateway's `update.check` the update gate reads:
 * whether the server refuses this app until it updates, the versions on
 * both sides, and the newest release, empty when the release lookup failed.
 */
data class GateCheck(
    val required: Boolean,
    val clientVersion: String,
    val serverVersion: String,
    val minClientVersion: String,
    val latest: String,
)

/** What the gate shows: this app's version, the server's, the oldest it accepts, and the tag to install. */
data class Gate(
    val clientVersion: String,
    val serverVersion: String,
    val minClientVersion: String,
    val tag: String,
)

private val HANDSHAKE_ORIGIN = Regex("^(http://127\\.0\\.0\\.1:\\d{1,5})/\\?token=([A-Za-z0-9_-]+)$")

/** A `/local/v1` call on the gateway: the URL to POST to and the bearer token it takes. */
data class GatewayCall(val url: String, val token: String)

/** The call of [verb] on the gateway whose handshake URL is [handshakeUrl], null for any other URL. */
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
    )
}

/**
 * The gate for [check], or null to let the dashboard through. The server
 * decides; this only refuses to gate on a version it cannot build a URL
 * from, so a dev build or a malformed answer never locks anyone out. The
 * newest release is what to install; when the lookup failed, the oldest one
 * the server accepts, which it only names once that release is published.
 */
fun gateFor(check: GateCheck?): Gate? {
    if (check == null || !check.required) return null
    if (!isReleaseTag(check.clientVersion) || !isReleaseTag(check.minClientVersion)) return null
    val tag = if (isReleaseTag(check.latest)) check.latest else check.minClientVersion
    return Gate(check.clientVersion, check.serverVersion, check.minClientVersion, tag)
}

/** What "Copy details" puts on the clipboard: everything someone helping needs, and the way out. */
fun gateDetails(gate: Gate, targets: List<UpdateTarget>): String =
    buildString {
        append("This Aether app (${gate.clientVersion}) is too old for the server ")
        append("(${gate.serverVersion} needs ${gate.minClientVersion} or newer).\n")
        append("Install ${gate.tag}:\n")
        for (target in targets) append(target.url).append('\n')
    }
