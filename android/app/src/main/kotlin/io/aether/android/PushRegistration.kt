package io.aether.android

import android.Manifest
import android.app.Activity
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.content.ContextCompat
import android.content.Context
import android.os.Handler
import android.os.Looper
import androidx.core.app.NotificationManagerCompat
import org.json.JSONObject
import org.unifiedpush.android.connector.INSTANCE_DEFAULT
import org.unifiedpush.android.connector.UnifiedPush

enum class PushKind(val wire: String) {
    ON("on"),
    OFF("off"),
    BLOCKED("blocked"),
    NO_DISTRIBUTOR("no-distributor"),

    /** Asking the distributor or the server; the answer follows. */
    PENDING("pending"),
}

data class PushState(val kind: PushKind, val endpoint: String? = null, val error: String? = null)

/**
 * Whether the switch may still ask for the notification permission: Android
 * 13 or later starts without it, and a refusal is final for the switch, since
 * Android stops showing the dialog after two.
 */
fun mayAskForPermission(sdk: Int, granted: Boolean, asked: Boolean): Boolean =
    sdk >= Build.VERSION_CODES.TIRAMISU && !granted && !asked

/**
 * The switch's state from what is stored and installed. [mayAsk] is whether
 * Android has yet to be asked for the notification permission. On means all of it
 * holds: a distributor still holds the registration, [server], the current
 * connection, has its endpoint, and Android lets the app notify.
 */
fun pushStateOf(
    stored: StoredPush,
    server: String?,
    pending: Boolean,
    registered: Boolean,
    anyDistributor: Boolean,
    allowed: Boolean,
    mayAsk: Boolean,
): PushState =
    when {
        pending -> PushState(PushKind.PENDING)
        // Turning the switch on asks for the permission Android 13 starts
        // without; anything else that keeps notifications off is a block.
        !allowed && !mayAsk -> PushState(PushKind.BLOCKED)
        stored.endpoint != null && registered && stored.synced && stored.server == server ->
            PushState(PushKind.ON, stored.endpoint)
        !anyDistributor -> PushState(PushKind.NO_DISTRIBUTOR, error = stored.error)
        else -> PushState(PushKind.OFF, error = stored.error)
    }

/**
 * This phone's push registration: a UnifiedPush distributor's endpoint with
 * Web Push keys, handed to the server's `push.subscribe` like a browser's
 * subscription. Every server call goes through [callServer], so it carries
 * the identity the dashboard in this app has, and no other.
 */
object PushRegistration {
    private val main = Handler(Looper.getMainLooper())

    @Volatile private var pending = false

    /** Which enable is current, so an earlier one's timeout cannot fail it. */
    private var attempt = 0

    /** Told, on the main thread, whenever [state] may have changed. */
    @Volatile var onChange: (() -> Unit)? = null

    fun state(context: Context): PushState =
        pushStateOf(
            context.storedPush(),
            context.pushServer(),
            pending,
            registered = UnifiedPush.getAckDistributor(context) != null,
            anyDistributor = UnifiedPush.getDistributors(context).isNotEmpty(),
            allowed = NotificationManagerCompat.from(context).areNotificationsEnabled(),
            mayAsk =
                mayAskForPermission(
                    Build.VERSION.SDK_INT,
                    granted =
                        ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) ==
                            PackageManager.PERMISSION_GRANTED,
                    asked = context.pushPermissionAsked(),
                ),
        )

    /**
     * Registers with the phone's distributor, asking which one when there are
     * several and none is the default. The server's VAPID key goes along, for
     * the distributors that require one. Call once notifications are allowed.
     */
    fun enable(activity: Activity) {
        val context = activity.applicationContext
        createPushChannels(context)
        pending = true
        context.storePush(context.storedPush().copy(error = null))
        changed()
        armTimeout(context)
        Thread {
                val vapid =
                    try {
                        context.callServer("push.status", JSONObject()).getString("public_key")
                    } catch (e: Exception) {
                        fail(context, "Could not read the server's push key: ${e.message}")
                        return@Thread
                    }
                main.post {
                    if (activity.isDestroyed) {
                        fail(context, "The app closed before a distributor was chosen.")
                        return@post
                    }
                    UnifiedPush.tryUseCurrentOrDefaultDistributor(activity) { chosen ->
                        if (chosen) {
                            UnifiedPush.register(context, INSTANCE_DEFAULT, null, vapid)
                        } else {
                            fail(context, "No push distributor was chosen. Pick one, or set a default in the distributor app.")
                        }
                    }
                }
            }
            .start()
    }

    /**
     * Stops notifications here: the server forgets the endpoint and the
     * distributor drops the registration. A server that cannot be told now
     * forgets it at its next push, when the distributor answers that the
     * endpoint is gone.
     */
    fun disable(context: Context) {
        val endpoint = context.storedPush().endpoint
        pending = true
        changed()
        Thread {
                val error =
                    try {
                        endpoint?.let { context.callServer("push.unsubscribe", JSONObject().put("endpoint", it)) }
                        null
                    } catch (e: Exception) {
                        "Turned off on this phone, but the server was not told: ${e.message}. " +
                            "It stops sending once the distributor reports the address gone."
                    }
                UnifiedPush.unregister(context, INSTANCE_DEFAULT)
                context.storePush(StoredPush(null, null, null, synced = false, error = error))
                pending = false
                changed()
            }
            .start()
    }

    fun endpointChanged(context: Context, endpoint: String, p256dh: String, auth: String) {
        context.storePush(StoredPush(endpoint, p256dh, auth, synced = false, error = context.storedPush().error, server = null))
        sync(context)
    }

    /**
     * Hands a stored endpoint the server does not have yet to `push.subscribe`.
     * Called again whenever the app can reach the server, so an endpoint that
     * arrived while it could not still gets there. After the connection
     * changed, it moves the registration instead.
     */
    fun sync(context: Context) {
        if (pending) return
        val stored = context.storedPush()
        val server = context.pushServer()
        if (stored.endpoint == null || stored.p256dh == null || stored.auth == null) return
        if (stored.synced && stored.server != null && stored.server != server) {
            move(context, stored)
            return
        }
        if (stored.synced && stored.server == server) return
        Thread {
                val keys = JSONObject().put("p256dh", stored.p256dh).put("auth", stored.auth)
                val error =
                    try {
                        context.callServer("push.subscribe", JSONObject().put("endpoint", stored.endpoint).put("keys", keys))
                        null
                    } catch (e: Exception) {
                        "The server did not take this phone's push endpoint: ${e.message}"
                    }
                // A newer endpoint may have arrived meanwhile; only this one is settled.
                val now = context.storedPush()
                if (now.endpoint == stored.endpoint) {
                    context.storePush(now.copy(synced = error == null, error = error ?: now.error, server = server))
                }
                pending = false
                changed()
            }
            .start()
    }

    /**
     * Gives the newly chosen server a registration of its own. The old one
     * is dropped at the distributor, so nothing the previous server sends
     * reaches this phone again, and the previous tailnet server is told to
     * forget it; an Edge server cannot be reached once another is chosen.
     */
    private fun move(context: Context, stored: StoredPush) {
        pending = true
        changed()
        armTimeout(context)
        Thread {
                val previous = stored.server
                val forgotten =
                    if (previous == null || !previous.startsWith("https://")) {
                        null
                    } else {
                        try {
                            callDashboard(previous, "push.unsubscribe", JSONObject().put("endpoint", stored.endpoint))
                            null
                        } catch (e: Exception) {
                            "The previous server was not told to forget this phone: ${e.message}"
                        }
                    }
                UnifiedPush.unregister(context, INSTANCE_DEFAULT)
                context.storePush(StoredPush(null, null, null, synced = false, error = forgotten))
                val vapid =
                    try {
                        context.callServer("push.status", JSONObject()).getString("public_key")
                    } catch (e: Exception) {
                        fail(context, listOfNotNull(forgotten, "Could not read the server's push key: ${e.message}").joinToString(" "))
                        return@Thread
                    }
                main.post { UnifiedPush.register(context, INSTANCE_DEFAULT, null, vapid) }
            }
            .start()
    }

    private fun armTimeout(context: Context) {
        val mine = ++attempt
        main.postDelayed({
            if (pending && attempt == mine) fail(context, "The push distributor did not answer within a minute.")
        }, 60_000)
    }

    fun clearError(context: Context) {
        context.storePush(context.storedPush().copy(error = null))
    }

    fun fail(context: Context, error: String) {
        context.storePush(context.storedPush().copy(error = error))
        pending = false
        changed()
    }

    fun unregistered(context: Context) {
        val endpoint = context.storedPush().endpoint
        context.storePush(StoredPush(null, null, null, synced = false, error = null))
        pending = false
        changed()
        if (endpoint == null) return
        Thread {
                try {
                    context.callServer("push.unsubscribe", JSONObject().put("endpoint", endpoint))
                } catch (e: Exception) {
                    fail(context, "The push distributor dropped Aether, and the server was not told: ${e.message}")
                }
            }
            .start()
    }

    private fun changed() {
        main.post { onChange?.invoke() }
    }
}
