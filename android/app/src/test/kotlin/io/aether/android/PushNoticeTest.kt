package io.aether.android

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

private fun push(json: String) = parsePush(json.toByteArray())

// What internal/push sends for an assistant card (#722) and for a run.
private const val CARD =
    """{"title":"Assistant asks for approval","body":"Close run-7k2m9q4xbd","url":"/?assistant=1","tag":"card:ABCDEFGHIJKLMNOPQRSTUVWXYZ","card":"ABCDEFGHIJKLMNOPQRSTUVWXYZ"}"""
private const val RUN = """{"title":"fix the build","body":"Question from the agent","run":"run-7k2m9q4xbd"}"""

class PushNoticeTest {
    @Test
    fun cardPushOffersApproveAndDeclineOverTheTailnet() {
        val notice = push(CARD)
        assertEquals("card:ABCDEFGHIJKLMNOPQRSTUVWXYZ", notice.tag)
        assertEquals(listOf(PushAction.APPROVE, PushAction.DECLINE), pushActions(notice, declinesDirectly = true))
        assertEquals("/?assistant=1", pushPath(notice))
    }

    @Test
    fun noButtonsWithoutTheCardOverTheLimitOnAClearOrInEdgeMode() {
        // A private or long card arrives without its id.
        assertEquals(emptyList<PushAction>(), pushActions(push(RUN), declinesDirectly = true))
        val long = CARD.replace("Close run-7k2m9q4xbd", "é".repeat(MAX_CARD_RUNES + 1))
        assertEquals(emptyList<PushAction>(), pushActions(push(long), declinesDirectly = true))
        val atLimit = CARD.replace("Close run-7k2m9q4xbd", "é".repeat(MAX_CARD_RUNES))
        assertEquals(2, pushActions(push(atLimit), declinesDirectly = true).size)
        val clear = CARD.replace("\"card\":", "\"clear\":true,\"card\":")
        assertEquals(emptyList<PushAction>(), pushActions(push(clear), declinesDirectly = true))
        assertEquals(emptyList<PushAction>(), pushActions(push(CARD), declinesDirectly = false))
    }

    @Test
    fun tagAndPathFollowTheServiceWorker() {
        val run = push(RUN)
        assertEquals("run-7k2m9q4xbd", run.tag)
        assertEquals("/?run=run-7k2m9q4xbd", pushPath(run))
        val budget = push("""{"title":"ws","body":"Budget warning","workspace":"ws_1"}""")
        assertEquals("budget:ws_1", budget.tag)
        assertEquals("/?page=workspace&id=ws_1", pushPath(budget))
        val cleared = push("""{"title":"t","body":"No longer needs you","run":"run-7k2m9q4xbd","clear":true}""")
        assertTrue(cleared.clear)
        assertEquals("run-7k2m9q4xbd", cleared.tag)
        assertTrue(push("""{"title":"t","body":"b","run":"run-7k2m9q4xbd","notice":true,"quiet":true}""").let { it.notice && it.quiet })
    }

    @Test
    fun needsGoToTheirOwnChannelAndUpdatesToTheOther() {
        assertEquals(PushChannel.NEEDS_YOU, pushChannel(push(CARD)))
        assertEquals(PushChannel.NEEDS_YOU, pushChannel(push(RUN)))
        // A private card has no id but is still a card.
        assertEquals(PushChannel.NEEDS_YOU, pushChannel(push(CARD.replace(",\"card\":\"ABCDEFGHIJKLMNOPQRSTUVWXYZ\"", ""))))
        assertEquals(PushChannel.UPDATES, pushChannel(push("""{"title":"t","body":"b","run":"run-7k2m9q4xbd","notice":true}""")))
        assertEquals(PushChannel.UPDATES, pushChannel(push("""{"title":"ws","body":"Budget warning","workspace":"ws_1"}""")))
        assertEquals(PushChannel.UPDATES, pushChannel(push("""{"title":"Aether","body":"Notifications work on this device."}""")))
    }

    @Test
    fun unreadableOrHostileFieldsFallBack() {
        val unreadable = parsePush(byteArrayOf(0x00, 0x7f))
        assertEquals("Aether", unreadable.title)
        assertEquals("A session needs you.", unreadable.body)
        assertEquals("aether", unreadable.tag)
        val hostile =
            push("""{"title":1,"url":"https://evil.example/","run":"../../x","card":"a b","workspace":"a/b"}""")
        assertEquals("Aether", hostile.title)
        assertNull(hostile.url)
        assertNull(hostile.run)
        assertNull(hostile.card)
        assertNull(hostile.workspace)
        assertEquals("/", pushPath(hostile))
        assertFalse(push("""{"clear":true}""").clear)
    }

    @Test
    fun onlyDashboardRoutesAreOpened() {
        assertTrue(isDashboardPath("/"))
        assertTrue(isDashboardPath("/?run=run-7k2m9q4xbd"))
        for (path in listOf("//evil.example/", "/settings", "https://x/", "/?a=1#b", "/?a b", "/?a\\b", "/?a\n")) {
            assertFalse(path, isDashboardPath(path))
        }
    }

    @Test
    fun pathsJoinBothDashboards() {
        assertEquals("https://h.ts.net/?run=r", dashboardAt("https://h.ts.net/", "/?run=r"))
        assertEquals("http://127.0.0.1:4000/?token=t&run=r", dashboardAt("http://127.0.0.1:4000/?token=t", "/?run=r"))
        assertEquals("https://h.ts.net/", dashboardAt("https://h.ts.net/", "/"))
        assertEquals("&run=r", edgeSuffix("/?run=r"))
        assertEquals("", edgeSuffix("/"))
    }

    @Test
    fun rpcAddressTokenAndOriginComeFromTheDashboardUrl() {
        assertEquals("https://h.ts.net/api/v1/push.subscribe" to null, rpcEndpoint("https://h.ts.net/", "push.subscribe"))
        assertEquals(
            "http://127.0.0.1:4000/api/v1/assistant.confirm" to "abc_-1",
            rpcEndpoint("http://127.0.0.1:4000/?token=abc_-1", "assistant.confirm"),
        )
        assertEquals("http://127.0.0.1:4000", originOf("http://127.0.0.1:4000/?token=abc"))
        assertEquals("https://h.ts.net:8443", originOf("https://h.ts.net:8443/"))
    }

    @Test
    fun aRefusedPermissionStaysBlocked() {
        val t = android.os.Build.VERSION_CODES.TIRAMISU
        assertEquals(true, mayAskForPermission(t, granted = false, asked = false))
        assertEquals(false, mayAskForPermission(t, granted = false, asked = true))
        assertEquals(false, mayAskForPermission(t, granted = true, asked = false))
        assertEquals(false, mayAskForPermission(t - 1, granted = false, asked = false))
        val none = StoredPush(null, null, null, synced = false, error = null)
        val asked = mayAskForPermission(t, granted = false, asked = true)
        assertEquals(PushKind.BLOCKED, pushStateOf(none, "edge", false, false, true, false, asked).kind)
    }

    @Test
    fun switchIsOnOnlyWhenEveryPartHolds() {
        val server = "https://h.ts.net/"
        val synced = StoredPush("https://ntfy.sh/upX?up=1", "k", "a", synced = true, error = null, server = server)
        fun state(
            stored: StoredPush = synced,
            current: String? = server,
            pending: Boolean = false,
            registered: Boolean = true,
            any: Boolean = true,
            allowed: Boolean = true,
            mayAsk: Boolean = false,
        ) = pushStateOf(stored, current, pending, registered, any, allowed, mayAsk)
        assertEquals(PushState(PushKind.ON, synced.endpoint), state())
        // Another server was chosen under Connection: it does not have the endpoint yet.
        assertEquals(PushKind.OFF, state(current = "edge").kind)
        assertEquals(PushKind.PENDING, state(pending = true).kind)
        assertEquals(PushKind.BLOCKED, state(allowed = false).kind)
        // The distributor was uninstalled under the registration.
        assertEquals(PushKind.NO_DISTRIBUTOR, state(registered = false, any = false).kind)
        val unsent = synced.copy(synced = false, error = "HTTP 503")
        assertEquals(PushState(PushKind.OFF, error = "HTTP 503"), state(stored = unsent))
        val none = StoredPush(null, null, null, synced = false, error = null)
        // Android 13 starts without the permission: the switch asks for it.
        assertEquals(PushKind.OFF, state(stored = none, allowed = false, mayAsk = true).kind)
        // Turned off in Android's settings, before or after subscribing.
        assertEquals(PushKind.BLOCKED, state(stored = none, allowed = false).kind)
    }
}
