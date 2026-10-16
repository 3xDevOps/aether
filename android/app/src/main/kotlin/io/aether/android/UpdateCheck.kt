package io.aether.android

/** The fields of `aether update --check --json` the update notice reads. */
data class UpdateCheck(val version: String, val latest: String, val updateAvailable: Boolean)

/**
 * What the update notice keeps between starts: when it last ran a check,
 * when one last succeeded and the line it printed, since when this install
 * has been behind a release (0 while current), and the tag the member
 * dismissed.
 */
data class StoredUpdate(
    val checkedAt: Long = 0,
    val answeredAt: Long = 0,
    val line: String? = null,
    val behindSince: Long = 0,
    val dismissed: String? = null,
)

const val CHECK_INTERVAL_MS = 60 * 60 * 1000L

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

/**
 * Whether to run a check now: an hour after the last one, failed or not,
 * when the clock went back, or once the app was updated after it.
 */
fun checkDue(now: Long, checkedAt: Long, appUpdatedAt: Long): Boolean =
    now - checkedAt !in 0L until CHECK_INTERVAL_MS || appUpdatedAt > checkedAt

/**
 * [stored] after a check at [now] that printed [line], or failed with null.
 * A failed check keeps the last answer and waits for the next interval.
 */
fun afterCheck(stored: StoredUpdate, now: Long, line: String?, appUpdatedAt: Long): StoredUpdate {
    val check = line?.let(::parseUpdateCheck) ?: return stored.copy(checkedAt = now)
    val stillBehind = stored.behindSince != 0L && stored.behindSince >= appUpdatedAt
    return stored.copy(
        checkedAt = now,
        answeredAt = now,
        line = line,
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
 * A Play install waits [PLAY_DELAY_MS] after it first fell behind.
 */
fun noticeFor(stored: StoredUpdate, source: InstallSource, now: Long, appUpdatedAt: Long): UpdateCheck? {
    if (stored.answeredAt < appUpdatedAt) return null
    val check = stored.line?.let(::parseUpdateCheck) ?: return null
    if (!check.updateAvailable || !isReleaseTag(check.latest) || check.latest == stored.dismissed) return null
    if (source == InstallSource.PLAY && now - stored.behindSince < PLAY_DELAY_MS) return null
    return check
}
