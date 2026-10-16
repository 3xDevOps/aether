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

// A check with no jitter that, when it fails, failed offline.
private fun answer(stored: StoredUpdate, now: Long, line: String?, appUpdatedAt: Long): StoredUpdate =
    afterCheck(stored, now, line, "dial tcp: lookup github.com: no such host", appUpdatedAt, 0.0)

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
    fun checksOnceTheNextCheckIsDue() {
        val now = 100 * DAY
        assertTrue(checkDue(now, StoredUpdate(), 0))
        val checked = answer(StoredUpdate(), now - HOUR + 1, line("v1.2.3", false), 0)
        assertFalse(checkDue(now, checked, 0))
        assertTrue(checkDue(now + 1, checked, 0))
        // A clock set back must not silence checks until it catches up.
        assertTrue(checkDue(now, checked.copy(checkedAt = now + DAY, nextCheckAt = now + DAY + HOUR), 0))
        // The app was updated since: the last answer is about the old version.
        assertTrue(checkDue(now, checked, checked.checkedAt + 1))
    }

    @Test
    fun theNextCheckLandsWithinTenPercentOfAnHour() {
        assertEquals(10 + HOUR * 9 / 10, afterCheck(StoredUpdate(), 10, null, "", 0, -0.1).nextCheckAt)
        assertEquals(10 + HOUR * 11 / 10, afterCheck(StoredUpdate(), 10, line("v1.2.3", false), "", 0, 0.1).nextCheckAt)
        assertEquals(10 + HOUR * 11 / 10, afterCheck(StoredUpdate(), 10, null, "", 0, 5.0).nextCheckAt)
    }

    @Test
    fun aFailedCheckKeepsTheLastAnswerAndRecordsTheError() {
        val before = answer(StoredUpdate(), 10, line("v1.3.0", true), 0)
        val after = answer(before, 20, null, 0)
        assertEquals(before.copy(checkedAt = 20, nextCheckAt = 20 + HOUR, error = "dial tcp: lookup github.com: no such host"), after)
        assertFalse(checkDue(20 + HOUR - 1, after, 0))
        assertEquals("the check printed nothing", afterCheck(before, 20, null, " \n", 0, 0.0).error)
        // The next success clears it.
        assertNull(answer(after, 30, line("v1.3.0", true), 0).error)
    }

    @Test
    fun aFailureShowsOnceTheLastAnswerIsADayOld() {
        val answered = answer(StoredUpdate(), 10, line("v1.2.3", false), 0)
        val failed = answer(answered, 20, null, 0)
        assertNull(checkFailure(answered, 10 + 2 * DAY))
        assertNull(checkFailure(failed, 10 + DAY - 1))
        assertEquals("dial tcp: lookup github.com: no such host", checkFailure(failed, 10 + DAY))
        // Never answered at all counts as old.
        assertEquals("dial tcp: lookup github.com: no such host", checkFailure(answer(StoredUpdate(), 100 * DAY, null, 0), 100 * DAY))
        // Dismissed stays dismissed until a check succeeds and a later one fails.
        val dismissed = failed.copy(failureDismissedAt = 10 + DAY)
        assertNull(checkFailure(answer(dismissed, 10 + DAY + HOUR, null, 0), 10 + 2 * DAY))
        val again = answer(answer(dismissed, 10 + 2 * DAY, line("v1.2.3", false), 0), 10 + 2 * DAY + 1, null, 0)
        assertNull(checkFailure(again, 10 + 3 * DAY - 1))
        assertEquals("dial tcp: lookup github.com: no such host", checkFailure(again, 10 + 3 * DAY))
    }

    @Test
    fun remembersSinceWhenThisInstallIsBehind() {
        val first = answer(StoredUpdate(), 10, line("v1.3.0", true), 0)
        assertEquals(10L, first.behindSince)
        // A newer release does not restart the wait.
        assertEquals(10L, answer(first, 30, line("v1.4.0", true), 0).behindSince)
        assertEquals(0L, answer(first, 30, line("v1.3.0", false, "v1.3.0"), 0).behindSince)
        // Behind again after the app updated: a new wait.
        assertEquals(40L, answer(first, 40, line("v1.4.0", true, "v1.3.0"), 20).behindSince)
    }

    @Test
    fun showsANewerReleaseUntilThatVersionIsDismissed() {
        val stored = answer(StoredUpdate(), 10, line("v1.3.0", true), 0)
        assertEquals("v1.3.0", noticeFor(stored, InstallSource.RELEASE, 10, 0)?.latest)

        val dismissed = stored.copy(dismissed = "v1.3.0")
        assertNull(noticeFor(dismissed, InstallSource.RELEASE, 10, 0))

        val newer = answer(dismissed, 20, line("v1.4.0", true), 0)
        assertEquals("v1.4.0", noticeFor(newer, InstallSource.RELEASE, 20, 0)?.latest)
    }

    @Test
    fun showsNothingWhenCurrentOrTheTagIsNotARelease() {
        assertNull(noticeFor(answer(StoredUpdate(), 10, line("v1.2.3", false), 0), InstallSource.RELEASE, 10, 0))
        assertNull(noticeFor(StoredUpdate(checkedAt = 10, line = line("v1.3.0/x", true)), InstallSource.RELEASE, 10, 0))
        assertNull(noticeFor(StoredUpdate(), InstallSource.RELEASE, 10, 0))
    }

    @Test
    fun anAnswerFromBeforeTheAppUpdatedIsNotShown() {
        val stored = answer(StoredUpdate(), 10, line("v1.3.0", true), 0)
        assertNull(noticeFor(stored, InstallSource.RELEASE, 30, 20))
        // Offline after the update: the failed check must not revive the old answer.
        val failed = answer(stored, 30, null, 20)
        assertNull(noticeFor(failed, InstallSource.RELEASE, 30, 20))
    }

    @Test
    fun aPlayInstallWaitsAWeekFromFallingBehind() {
        val stored = answer(StoredUpdate(), 10, line("v1.3.0", true), 0)
        assertNull(noticeFor(stored, InstallSource.PLAY, 10 + 7 * DAY - 1, 0))
        assertEquals("v1.3.0", noticeFor(stored, InstallSource.PLAY, 10 + 7 * DAY, 0)?.latest)

        // Daily releases cannot keep the notice away.
        var daily = stored
        for (day in 1..7) daily = answer(daily, 10 + day * DAY, line("v1.3.$day", true), 0)
        assertEquals("v1.3.7", noticeFor(daily, InstallSource.PLAY, 10 + 7 * DAY, 0)?.latest)
    }
}
