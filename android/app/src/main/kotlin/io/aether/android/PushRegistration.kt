package io.aether.android

import android.app.Activity
import android.content.Context
import android.os.Handler
import android.os.Looper
import android.util.Log
import androidx.core.app.NotificationManagerCompat
import org.json.JSONObject
import org.unifiedpush.android.connector.INSTANCE_DEFAULT
import org.unifiedpush.android.connector.UnifiedPush

private const val TAG = "aether"

/** What the dashboard's push switch shows for this phone. */
enum class PushKind(val wire: String) {
    ON("on"),
    OFF("off"),

    /** Android blocks the app's notifications. */
    BLOCKED("blocked"),

    /** No UnifiedPush distributor is installed to deliver them. */
    NO_DISTRIBUTOR("no-distributor"),

    /** Asking the distributor or the server; the answer follows. */
    PENDING("pending"),
}

/** [error] is why the last attempt failed, in the failing side's own words. */
data class PushState(val kind: PushKind, val endpoint: String? = null, val error: String? = null)

/**
 * The switch's state from what is stored and installed. On means all of it
 * holds: a distributor still holds the registration, the server has its
 * endpoint, and Android lets the app notify.
 */
fun pushStateOf(
    stored: StoredPush,
    pending: Boolean,
    registered: Boolean,
    anyDistributor: Boolean,
    allowed: Boolean,
): PushState =
    when {
        pending -> PushState(PushKind.PENDING)
        stored.endpoint != null && registered && !allowed -> PushState(PushKind.BLOCKED)
        stored.endpoint != null && registered && stored.synced -> PushState(PushKind.ON, stored.endpoint)
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

    /** Told, on the main thread, whenever [state] may have changed. */
    @Volatile var onChange: (() -> Unit)? = null

    fun state(context: Context): PushState =
        pushStateOf(
            context.storedPush(),
            pending,
            registered = UnifiedPush.getAckDistributor(context) != null,
            anyDistributor = UnifiedPush.getDistributors(context).isNotEmpty(),
            allowed = NotificationManagerCompat.from(context).areNotificationsEnabled(),
        )

    /**
     * Registers with the phone's distributor, asking which one when there are
     * several and none is the default. The server's VAPID key goes along, for
     * the distributors that require one. Call once notifications are allowed.
     */
    fun enable(activity: Activity) {
        val context = activity.applicationContext
        if (UnifiedPush.getDistributors(context).isEmpty()) {
            changed()
            return
        }
        pending = true
        context.storePush(context.storedPush().copy(error = null))
        changed()
        main.postDelayed({ if (pending) fail(context, "The push distributor did not answer within a minute.") }, 60_000)
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
                if (endpoint != null) {
                    try {
                        context.callServer("push.unsubscribe", JSONObject().put("endpoint", endpoint))
                    } catch (e: Exception) {
                        Log.w(TAG, "push: the server was not told to forget this phone: ${e.message}")
                    }
                }
                UnifiedPush.unregister(context, INSTANCE_DEFAULT)
                context.storePush(StoredPush(null, null, null, synced = false, error = null))
                pending = false
                changed()
            }
            .start()
    }

    /** The distributor gave a new endpoint: the server is told, now if it can be reached. */
    fun endpointChanged(context: Context, endpoint: String, p256dh: String, auth: String) {
        context.storePush(StoredPush(endpoint, p256dh, auth, synced = false, error = null))
        sync(context)
    }

    /**
     * Hands a stored endpoint the server does not have yet to `push.subscribe`.
     * Called again whenever the app can reach the server, so an endpoint that
     * arrived while it could not still gets there.
     */
    fun sync(context: Context) {
        val stored = context.storedPush()
        if (stored.endpoint == null || stored.p256dh == null || stored.auth == null || stored.synced) return
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
                if (now.endpoint == stored.endpoint) context.storePush(now.copy(synced = error == null, error = error))
                pending = false
                changed()
            }
            .start()
    }

    fun fail(context: Context, error: String) {
        context.storePush(context.storedPush().copy(error = error))
        pending = false
        changed()
    }

    /** The distributor dropped the registration, as when it is uninstalled. */
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
                    Log.w(TAG, "push: the server was not told to forget this phone: ${e.message}")
                }
            }
            .start()
    }

    private fun changed() {
        main.post { onChange?.invoke() }
    }
}
