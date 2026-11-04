package io.aether.android

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import org.json.JSONObject
import org.unifiedpush.android.connector.FailedReason
import org.unifiedpush.android.connector.PushService
import org.unifiedpush.android.connector.data.PushEndpoint
import org.unifiedpush.android.connector.data.PushMessage

private const val TAG = "aether"
private const val CHANNEL = "needs-you"
private const val ID = 1
private const val EXTRA_KEEP = "io.aether.android.keep"

/** MainActivity opens [EXTRA_PATH] on the dashboard. */
const val ACTION_OPEN = "io.aether.android.OPEN"
const val EXTRA_PATH = "io.aether.android.PATH"

private const val ACTION_DECLINE = "io.aether.android.DECLINE"
private const val EXTRA_CARD = "io.aether.android.CARD"
private const val EXTRA_TAG = "io.aether.android.TAG"
private const val EXTRA_TITLE = "io.aether.android.TITLE"

/**
 * Receives from the phone's UnifiedPush distributor. Only the connector in
 * this app binds it. A message reaches [onMessage] decrypted only if it was
 * encrypted to this registration's keys, which only the server holds, so
 * anything else is dropped rather than shown.
 */
class AetherPushService : PushService() {
    override fun onNewEndpoint(endpoint: PushEndpoint, instance: String) {
        val keys = endpoint.pubKeySet
        if (keys == null) {
            PushRegistration.fail(this, "The push distributor gave an endpoint without Web Push keys.")
            return
        }
        PushRegistration.endpointChanged(this, endpoint.url, keys.pubKey, keys.auth)
    }

    override fun onMessage(message: PushMessage, instance: String) {
        if (!message.decrypted) {
            Log.w(TAG, "push: dropped a message that was not encrypted to this phone")
            return
        }
        showPush(this, parsePush(message.content))
    }

    override fun onRegistrationFailed(reason: FailedReason, instance: String) {
        PushRegistration.fail(this, "The push distributor refused to register Aether: $reason")
    }

    override fun onUnregistered(instance: String) {
        PushRegistration.unregistered(this)
    }
}

/**
 * Shows [notice] as the service worker would: one notification per tag, a
 * clear removes it unless it is an assistant's notice or the outcome of a
 * decision made here. Nothing shows while Android blocks the app.
 */
fun showPush(context: Context, notice: PushNotice) {
    val manager = NotificationManagerCompat.from(context)
    if (notice.clear) {
        val current = manager.activeNotifications.firstOrNull { it.tag == notice.tag && it.id == ID }
        if (current?.notification?.extras?.getBoolean(EXTRA_KEEP) != true) manager.cancel(notice.tag, ID)
        return
    }
    if (!manager.areNotificationsEnabled()) return
    val path = pushPath(notice)
    val open = openIntent(context, notice.tag, path)
    val builder =
        builder(context, notice.title, notice.body)
            .setContentIntent(open)
            .setAutoCancel(true)
            .setSilent(notice.quiet)
            .addExtras(android.os.Bundle().apply { putBoolean(EXTRA_KEEP, notice.notice) })
    val declinesDirectly = context.connectionMode() == ConnectionMode.TAILSCALE
    for (action in pushActions(notice, declinesDirectly)) {
        when (action) {
            PushAction.APPROVE -> builder.addAction(0, context.getString(R.string.push_approve), open)
            PushAction.DECLINE -> builder.addAction(0, context.getString(R.string.push_decline), declineIntent(context, notice))
        }
    }
    notify(manager, notice.tag, builder)
}

private fun builder(context: Context, title: String, body: String): NotificationCompat.Builder {
    val manager = context.getSystemService(NotificationManager::class.java)
    if (manager.getNotificationChannel(CHANNEL) == null) {
        manager.createNotificationChannel(
            NotificationChannel(CHANNEL, context.getString(R.string.push_channel), NotificationManager.IMPORTANCE_HIGH),
        )
    }
    return NotificationCompat.Builder(context, CHANNEL)
        .setSmallIcon(R.mipmap.ic_launcher_foreground)
        .setContentTitle(title)
        .setContentText(body)
        .setStyle(NotificationCompat.BigTextStyle().bigText(body))
        .setCategory(NotificationCompat.CATEGORY_MESSAGE)
}

// areNotificationsEnabled was checked; Android can still revoke it between.
private fun notify(manager: NotificationManagerCompat, tag: String, builder: NotificationCompat.Builder) {
    try {
        manager.notify(tag, ID, builder.build())
    } catch (e: SecurityException) {
        Log.w(TAG, "push: not allowed to notify: ${e.message}")
    }
}

// Extras do not tell PendingIntents apart, and two tags can share a hash, so
// the full tag goes in the data each one is matched by.
private fun pushIdentity(tag: String): Uri = Uri.fromParts("aether-push", tag, null)

private fun openIntent(context: Context, tag: String, path: String): PendingIntent {
    val intent =
        Intent(context, MainActivity::class.java)
            .setAction(ACTION_OPEN)
            .setData(pushIdentity(tag))
            .putExtra(EXTRA_PATH, path)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
    return PendingIntent.getActivity(
        context,
        0,
        intent,
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )
}

private fun declineIntent(context: Context, notice: PushNotice): PendingIntent {
    val intent =
        Intent(context, DeclineReceiver::class.java)
            .setAction(ACTION_DECLINE)
            .setData(pushIdentity(notice.tag))
            .putExtra(EXTRA_CARD, notice.card)
            .putExtra(EXTRA_TAG, notice.tag)
            .putExtra(EXTRA_TITLE, notice.title)
    return PendingIntent.getBroadcast(
        context,
        0,
        intent,
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )
}

/**
 * Declines a card from its notification without opening the app, with
 * `assistant.confirm` over the tailnet dashboard: the same call the service
 * worker makes, identified the same way, by this phone's tailnet login. Not
 * exported: only this app's own notification can send it. The outcome, or
 * the server's own refusal, replaces the notification.
 */
class DeclineReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val card = intent.getStringExtra(EXTRA_CARD) ?: return
        val tag = intent.getStringExtra(EXTRA_TAG) ?: return
        val title = intent.getStringExtra(EXTRA_TITLE) ?: context.getString(R.string.app_name)
        val app = context.applicationContext
        val result = goAsync()
        Thread {
                val body =
                    try {
                        // goAsync's receiver has 10 seconds in all.
                        app.callServer(
                            "assistant.confirm",
                            JSONObject().put("confirmation_id", card).put("approve", false),
                            timeoutMs = 4_000,
                        )
                        app.getString(R.string.push_declined)
                    } catch (e: Exception) {
                        app.getString(R.string.push_not_decided, e.message)
                    }
                val builder =
                    builder(app, title, body)
                        .setSilent(true)
                        .setAutoCancel(true)
                        .setContentIntent(openIntent(app, tag, CARD_PATH))
                        .addExtras(android.os.Bundle().apply { putBoolean(EXTRA_KEEP, true) })
                notify(NotificationManagerCompat.from(app), tag, builder)
                result.finish()
            }
            .start()
    }
}

// internal/push's CardLink: the dashboard with the assistant panel open.
private const val CARD_PATH = "/?assistant=1"
