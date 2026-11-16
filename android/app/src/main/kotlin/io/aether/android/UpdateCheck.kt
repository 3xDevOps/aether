package io.aether.android

/** The fields of `aether update --check --json` the update notice reads. */
data class UpdateCheck(val version: String, val latest: String, val updateAvailable: Boolean)

/**
 * What the update notice keeps between starts: when it last ran a check and
 * when the next one is due, when one last succeeded and the line it printed,
 * what the last one failed with (null when it succeeded), since when this
 * install has been behind a release (0 while current), the tag the member
 * dismissed, and when they last dismissed a failure.
 */
data class StoredUpdate(
    val checkedAt: Long = 0,
    val nextCheckAt: Long = 0,
    val answeredAt: Long = 0,
    val line: String? = null,
    val error: String? = null,
    val behindSince: Long = 0,
    val dismissed: String? = null,
    val failureDismissedAt: Long = 0,
    val via: String = VIA_CLI,
)

/** Where a check ran: the bundled CLI, or the gateway's `update.check`. A stored answer is only good for the one that made it. */
const val VIA_CLI = "cli"
const val VIA_GATEWAY = "gateway"

const val CHECK_INTERVAL_MS = 60 * 60 * 1000L

/** How far either side of [CHECK_INTERVAL_MS] the next check lands, so phones do not check in step. */
const val CHECK_JITTER = 0.1

/** How old the last answer must be before a failed check is shown. */
const val STALE_ANSWER_MS = 24 * 60 * 60 * 1000L

/**
 * How long a Play install must have been behind a release before it hears
 * of one. GitHub publishes a release before Play finishes reviewing and
 * rolling it out, and Play is the only one that can install it. Measured
 * from the first release it fell behind, so frequent releases cannot keep
 * restarting the wait.
 */
const val PLAY_DELAY_MS = 7 * 24 * 60 * 60 * 1000L

// The check is json.Marshal of selfupdate.Check on one line. org.json is a
// stub in the unit tests' android.jar, so each field is matched on its own,
// and a value with a backslash is not one this notice shows.
private fun stringField(line: String, name: String): String? =
    Regex("\"$name\"\\s*:\\s*\"([^\"\\\\]*)\"").find(line)?.groupValues?.get(1)

/** The check in [line], or null when [line] is not one. */
fun parseUpdateCheck(line: String): UpdateCheck? {
    val json = line.trim()
    if (!json.startsWith("{") || !json.endsWith("}")) return null
    val version = stringField(json, "version") ?: return null
    val available =
        Regex("\"update_available\"\\s*:\\s*(true|false)").find(json)?.groupValues?.get(1)
            ?: return null
    // Omitted when no check ran: a dev build, or AETHER_NO_UPDATE_CHECK.
    val latest = stringField(json, "latest") ?: ""
    return UpdateCheck(version, latest, available == "true")
}

private val DESCRIBE_TAIL = Regex("-[0-9]+-g[0-9a-f]{4,}(-dirty)?$|-dirty$|\\+.*$")

/**
 * The check line for an `update.check` [body] the gateway answered. The
 * app only moves to its server's release: the gateway's target counts when
 * it is that release, and any newer one on the release channel waits for
 * the server to update first. The run is a failure when [body] is not a
 * check or the gateway could not reach the server, which says nothing
 * about this app.
 */
fun serverReleaseCheck(body: String): CheckRun {
    val check = parseGateCheck(body) ?: return CheckRun(null, "update.check answered: ${body.trim()}", VIA_GATEWAY)
    if (check.serverVersion.isEmpty()) {
        return CheckRun(null, "update.check did not reach the server: ${check.serverError}", VIA_GATEWAY)
    }
    val offered = check.targetVersion.takeIf { it == check.serverVersion.replace(DESCRIBE_TAIL, "") }.orEmpty()
    val latest = offered.ifEmpty { check.clientVersion }
    val line = "{\"version\":\"${check.clientVersion}\",\"latest\":\"$latest\",\"update_available\":${offered.isNotEmpty()}}"
    return CheckRun(line, "", VIA_GATEWAY)
}

/**
 * Whether to run a check now: once the next one is due, failed or not, when
 * the clock went back, or once the app was updated after the last one.
 */
fun checkDue(now: Long, stored: StoredUpdate, appUpdatedAt: Long, via: String = stored.via): Boolean =
    now !in stored.checkedAt until stored.nextCheckAt || appUpdatedAt > stored.checkedAt || via != stored.via

/**
 * [stored] after a check at [now] that printed [line], or failed with
 * [error]. A failed check keeps the last answer. The next check is due an
 * interval later, moved by [jitter], a fraction within [CHECK_JITTER].
 */
fun afterCheck(
    stored: StoredUpdate,
    now: Long,
    line: String?,
    error: String,
    appUpdatedAt: Long,
    jitter: Double,
    via: String = VIA_CLI,
): StoredUpdate {
    val next = now + (CHECK_INTERVAL_MS * (1 + jitter.coerceIn(-CHECK_JITTER, CHECK_JITTER))).toLong()
    val check =
        line?.let(::parseUpdateCheck)
            ?: return stored.copy(checkedAt = now, nextCheckAt = next, error = error.ifBlank { "the check printed nothing" }, via = via)
    val stillBehind = stored.behindSince != 0L && stored.behindSince >= appUpdatedAt
    return stored.copy(
        checkedAt = now,
        nextCheckAt = next,
        answeredAt = now,
        line = line,
        error = null,
        via = via,
        behindSince =
            when {
                !check.updateAvailable -> 0L
                stillBehind -> stored.behindSince
                else -> now
            },
    )
}

/**
 * The check the notice shows, or null for none: a newer release the member
 * has not dismissed, from a check answered since the app was last updated.
 * A Play install waits [PLAY_DELAY_MS] after it first fell behind, and
 * hears of stable releases only: Play carries no beta or nightly, so a
 * prerelease tag shows nothing there.
 */
fun noticeFor(stored: StoredUpdate, source: InstallSource, now: Long, appUpdatedAt: Long, via: String = stored.via): UpdateCheck? {
    if (stored.answeredAt < appUpdatedAt || stored.via != via) return null
    val check = stored.line?.let(::parseUpdateCheck) ?: return null
    if (!check.updateAvailable || !isReleaseTag(check.latest) || check.latest == stored.dismissed) return null
    if (source == InstallSource.PLAY && (check.latest.contains('-') || now - stored.behindSince < PLAY_DELAY_MS)) return null
    return check
}

/**
 * What the last check failed with, or null for nothing to show: only once
 * the last answer is [STALE_ANSWER_MS] old, so one offline hour stays quiet,
 * and not after the member dismissed it since that answer.
 */
fun checkFailure(stored: StoredUpdate, now: Long): String? {
    val error = stored.error ?: return null
    if (now - stored.answeredAt < STALE_ANSWER_MS || stored.failureDismissedAt > stored.answeredAt) return null
    return error
}
