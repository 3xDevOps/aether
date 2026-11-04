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
private const val CHANNEL_NEEDS_YOU = "needs-you"
private const val CHANNEL_UPDATES = "updates"
private const val ID = 1
private const val EXTRA_KEEP = "io.aether.android.keep"

/** MainActivity opens [EXTRA_PATH] on the dashboard. */
const val ACTION_OPEN = "io.aether.android.OPEN"
const val EXTRA_PATH = "io.aether.android.PATH"

private const val ACTION_DECLINE = "io.aether.android.DECLINE"
private const val EXTRA_CARD = "io.aether.android.CARD"
private const val EXTRA_TAG = "io.aether.android.TAG"
private const val EXTRA_TEXT = "io.aether.android.TEXT"

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
    val open = openIntent(context, notice.tag, pushPath(notice))
    val builder =
        builder(context, pushChannel(notice), notice.title, notice.body)
            .setContentIntent(open)
            .setAutoCancel(true)
            .setSilent(notice.quiet)
            .addExtras(android.os.Bundle().apply { putBoolean(EXTRA_KEEP, notice.notice) })
    val declinesDirectly = context.connectionMode() == ConnectionMode.TAILSCALE
    for (action in pushActions(notice, declinesDirectly)) {
        when (action) {
            PushAction.APPROVE -> builder.addAction(0, context.getString(R.string.push_approve), open)
            // A locked phone has to be unlocked first: Decline acts with
            // this phone's identity, and no one should do that from the
            // lock screen.
            PushAction.DECLINE ->
                builder.addAction(
                    NotificationCompat.Action.Builder(0, context.getString(R.string.push_decline), declineIntent(context, notice))
                        .setAuthenticationRequired(true)
                        .build(),
                )
        }
    }
    notify(manager, notice.tag, builder)
}

/**
 * Both channels, so Android's settings for the app list them from the
 * moment notifications are turned on, before the first push.
 */
fun createPushChannels(context: Context) {
    context.getSystemService(NotificationManager::class.java).createNotificationChannels(
        listOf(
            NotificationChannel(CHANNEL_NEEDS_YOU, context.getString(R.string.push_channel), NotificationManager.IMPORTANCE_HIGH),
            NotificationChannel(CHANNEL_UPDATES, context.getString(R.string.push_channel_updates), NotificationManager.IMPORTANCE_DEFAULT),
        ),
    )
}

/**
 * A notification on [channel]. The lock screen shows only that Aether has
 * something; the session and its text wait until the phone is unlocked.
 */
private fun builder(context: Context, channel: PushChannel, title: String, body: String): NotificationCompat.Builder {
    createPushChannels(context)
    val id = if (channel == PushChannel.NEEDS_YOU) CHANNEL_NEEDS_YOU else CHANNEL_UPDATES
    fun base() =
        NotificationCompat.Builder(context, id)
            .setSmallIcon(R.drawable.ic_stat_aether)
            .setColor(context.getColor(R.color.accent_fill))
    val public =
        base()
            .setContentTitle(context.getString(R.string.app_name))
            .setContentText(context.getString(R.string.push_public))
            .build()
    return base()
        .setContentTitle(title)
        .setContentText(body)
        .setStyle(NotificationCompat.BigTextStyle().bigText(body))
        .setCategory(if (channel == PushChannel.NEEDS_YOU) NotificationCompat.CATEGORY_REMINDER else null)
        .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
        .setPublicVersion(public)
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
            .putExtra(EXTRA_TEXT, notice.body)
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
 * exported: only this app's own notification can send it.
 *
 * The buttons go at once, so nothing can be tapped twice while the call
 * runs. The outcome names the card it was about; a refusal alerts, with the
 * server's own error, and opens the card.
 */
class DeclineReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val card = intent.getStringExtra(EXTRA_CARD) ?: return
        val tag = intent.getStringExtra(EXTRA_TAG) ?: return
        val text = intent.getStringExtra(EXTRA_TEXT).orEmpty()
        val app = context.applicationContext
        val manager = NotificationManagerCompat.from(app)
        fun outcome(title: String, body: String) =
            builder(app, PushChannel.NEEDS_YOU, title, body)
                .setAutoCancel(true)
                .setContentIntent(openIntent(app, tag, CARD_PATH))
                .addExtras(android.os.Bundle().apply { putBoolean(EXTRA_KEEP, true) })
        notify(manager, tag, outcome(text, app.getString(R.string.push_declining)).setSilent(true).setOnlyAlertOnce(true))
        val result = goAsync()
        Thread {
                val done =
                    try {
                        // goAsync's receiver has 10 seconds in all.
                        app.callServer(
                            "assistant.confirm",
                            JSONObject().put("confirmation_id", card).put("approve", false),
                            timeoutMs = 4_000,
                        )
                        outcome(app.getString(R.string.push_declined), text).setSilent(true)
                    } catch (e: Exception) {
                        outcome(app.getString(R.string.push_decline_failed), e.message ?: e.toString())
                    }
                notify(manager, tag, done)
                result.finish()
            }
            .start()
    }
}

// internal/push's CardLink: the dashboard with the assistant panel open.
private const val CARD_PATH = "/?assistant=1"
