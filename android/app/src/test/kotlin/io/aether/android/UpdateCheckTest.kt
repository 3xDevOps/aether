package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

private const val HOUR = CHECK_INTERVAL_MS
private const val DAY = 24 * HOUR

// What `aether update --check --json` prints, as json.Marshal writes it.
private fun line(latest: String?, available: Boolean, version: String = "v1.2.3"): String {
    val latestField = latest?.let { "\"latest\":\"$it\"," } ?: ""
    return "{\"version\":\"$version\",\"commit\":\"abc1234\",$latestField" +
        "\"update_available\":$available,\"release_url\":\"https://github.com/3xDevOps/Aether/releases/tag/$latest\"," +
        "\"dev\":false,\"disabled\":false,\"packaged\":\"Android app\",\"can_self_update\":false," +
        "\"checked_at\":\"2026-10-16T05:00:00Z\"}"
}

class UpdateCheckTest {
    @Test
    fun parsesTheCheckLine() {
        assertEquals(UpdateCheck("v1.2.3", "v1.3.0", true), parseUpdateCheck(line("v1.3.0", true)))
        assertEquals(UpdateCheck("v1.3.0", "v1.3.0", false), parseUpdateCheck(line("v1.3.0", false, "v1.3.0")))
    }

    @Test
    fun aDevBuildPrintsNoLatest() {
        assertEquals(UpdateCheck("dev", "", false), parseUpdateCheck(line(null, false, "dev")))
    }

    @Test
    fun anythingElseIsNotACheck() {
        for (text in listOf("", "update available: v1.3.0", "{\"version\":\"v1.2.3\"}", "{}", "[]")) {
            assertNull(text, parseUpdateCheck(text))
        }
    }

    @Test
    fun checksAtMostOnceAnHour() {
        val now = 100 * DAY
        assertTrue(checkDue(now, 0, 0))
        assertFalse(checkDue(now, now - HOUR + 1, 0))
        assertTrue(checkDue(now, now - HOUR, 0))
        // A clock set back must not silence checks until it catches up.
        assertTrue(checkDue(now, now + DAY, 0))
        // The app was updated since: the last answer is about the old version.
        assertTrue(checkDue(now, now - 1, now - 1 + 1))
    }

    @Test
    fun aFailedCheckWaitsForTheNextIntervalAndKeepsTheLastAnswer() {
        val before = afterCheck(StoredUpdate(), 10, line("v1.3.0", true), 0)
        val after = afterCheck(before, 20, null, 0)
        assertEquals(before.copy(checkedAt = 20), after)
        assertFalse(checkDue(20 + HOUR - 1, after.checkedAt, 0))
    }

    @Test
    fun remembersSinceWhenThisInstallIsBehind() {
        val first = afterCheck(StoredUpdate(), 10, line("v1.3.0", true), 0)
        assertEquals(10L, first.behindSince)
        // A newer release does not restart the wait.
        assertEquals(10L, afterCheck(first, 30, line("v1.4.0", true), 0).behindSince)
        assertEquals(0L, afterCheck(first, 30, line("v1.3.0", false, "v1.3.0"), 0).behindSince)
        // Behind again after the app updated: a new wait.
        assertEquals(40L, afterCheck(first, 40, line("v1.4.0", true, "v1.3.0"), 20).behindSince)
    }

    @Test
    fun showsANewerReleaseUntilThatVersionIsDismissed() {
        val stored = afterCheck(StoredUpdate(), 10, line("v1.3.0", true), 0)
        assertEquals("v1.3.0", noticeFor(stored, InstallSource.RELEASE, 10, 0)?.latest)

        val dismissed = stored.copy(dismissed = "v1.3.0")
        assertNull(noticeFor(dismissed, InstallSource.RELEASE, 10, 0))

        val newer = afterCheck(dismissed, 20, line("v1.4.0", true), 0)
        assertEquals("v1.4.0", noticeFor(newer, InstallSource.RELEASE, 20, 0)?.latest)
    }

    @Test
    fun showsNothingWhenCurrentOrTheTagIsNotARelease() {
        assertNull(noticeFor(afterCheck(StoredUpdate(), 10, line("v1.2.3", false), 0), InstallSource.RELEASE, 10, 0))
        assertNull(noticeFor(StoredUpdate(checkedAt = 10, line = line("v1.3.0/x", true)), InstallSource.RELEASE, 10, 0))
        assertNull(noticeFor(StoredUpdate(), InstallSource.RELEASE, 10, 0))
    }

    @Test
    fun anAnswerFromBeforeTheAppUpdatedIsNotShown() {
        val stored = afterCheck(StoredUpdate(), 10, line("v1.3.0", true), 0)
        assertNull(noticeFor(stored, InstallSource.RELEASE, 30, 20))
        // Offline after the update: the failed check must not revive the old answer.
        val failed = afterCheck(stored, 30, null, 20)
        assertNull(noticeFor(failed, InstallSource.RELEASE, 30, 20))
    }

    @Test
    fun aPlayInstallWaitsAWeekFromFallingBehind() {
        val stored = afterCheck(StoredUpdate(), 10, line("v1.3.0", true), 0)
        assertNull(noticeFor(stored, InstallSource.PLAY, 10 + 7 * DAY - 1, 0))
        assertEquals("v1.3.0", noticeFor(stored, InstallSource.PLAY, 10 + 7 * DAY, 0)?.latest)

        // Daily releases cannot keep the notice away.
        var daily = stored
        for (day in 1..7) daily = afterCheck(daily, 10 + day * DAY, line("v1.3.$day", true), 0)
        assertEquals("v1.3.7", noticeFor(daily, InstallSource.PLAY, 10 + 7 * DAY, 0)?.latest)
    }
}
