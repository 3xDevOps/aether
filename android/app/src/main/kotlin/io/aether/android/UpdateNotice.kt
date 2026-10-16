package io.aether.android

import android.app.Activity
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.net.ConnectivityManager
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.view.View
import android.widget.Button
import android.widget.TextView
import java.io.File
import java.io.IOException
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.Future
import java.util.concurrent.TimeUnit

private const val CHECK_TIMEOUT_SECONDS = 30L

/**
 * The banner that says a newer Aether release is out, in either mode. The
 * bundled CLI's `update --check --json` decides that, so the release lookup
 * and version ordering are the desktop's. Its one button opens Google Play
 * or the release page; nothing is downloaded or installed here.
 */
class UpdateNotice(private val activity: Activity, private val banner: View) {
    private val main = Handler(Looper.getMainLooper())
    private val executor: ExecutorService = Executors.newSingleThreadExecutor()
    private val source = installSourceFor(installerPackage(activity))
    private var check: Future<*>? = null
    private var destroyed = false
    private var shown = ""

    private val title = banner.findViewById<TextView>(R.id.update_title)
    private val running = banner.findViewById<TextView>(R.id.update_running)
    private val detail = banner.findViewById<TextView>(R.id.update_detail)
    private val error = banner.findViewById<TextView>(R.id.update_error)
    private val action = banner.findViewById<Button>(R.id.update_action)
    private val notes = banner.findViewById<Button>(R.id.update_notes)

    init {
        banner.findViewById<View>(R.id.update_dismiss).setOnClickListener {
            activity.storeUpdate(activity.storedUpdate().copy(dismissed = shown))
            banner.visibility = View.GONE
        }
        action.setOnClickListener {
            open(updateTargets(source, activity.packageName, shown).orEmpty())
        }
        notes.setOnClickListener {
            open(listOfNotNull(releaseNotesUrl(shown)?.let(::UpdateTarget)))
        }
    }

    /** Shows what the last check found, and runs a new one when it is due. */
    fun resume() {
        val stored = activity.storedUpdate()
        render(stored)
        if (check?.isDone == false) return
        if (!checkDue(System.currentTimeMillis(), stored.checkedAt, appUpdatedAt())) return
        val context = activity.applicationContext
        check =
            executor.submit {
                val line = runCheck(context)
                main.post {
                    if (destroyed) return@post
                    val next = afterCheck(activity.storedUpdate(), System.currentTimeMillis(), line)
                    activity.storeUpdate(next)
                    render(next)
                }
            }
    }

    fun destroy() {
        destroyed = true
        check?.cancel(true)
        executor.shutdownNow()
    }

    private fun render(stored: StoredUpdate) {
        val found = noticeFor(stored, source, System.currentTimeMillis(), appUpdatedAt())
        if (found == null) {
            banner.visibility = View.GONE
            return
        }
        if (found.latest != shown) error.visibility = View.GONE
        shown = found.latest
        val play = source == InstallSource.PLAY
        title.text = activity.getString(R.string.update_title, found.latest)
        running.text = activity.getString(R.string.update_running, found.version)
        detail.setText(if (play) R.string.update_play_detail else R.string.update_release_detail)
        action.setText(if (play) R.string.update_open_play else R.string.update_open_release)
        // A release install's button already opens the release page.
        notes.visibility = if (play) View.VISIBLE else View.GONE
        banner.visibility = View.VISIBLE
    }

    /** Opens the first of [targets] some app takes, else shows why none did. */
    private fun open(targets: List<UpdateTarget>) {
        val failures = mutableListOf<String>()
        for (target in targets) {
            val intent =
                Intent(Intent.ACTION_VIEW, Uri.parse(target.url)).addCategory(Intent.CATEGORY_BROWSABLE)
            target.packageName?.let(intent::setPackage)
            try {
                activity.startActivity(intent)
                error.visibility = View.GONE
                return
            } catch (e: ActivityNotFoundException) {
                failures += "${target.url}: ${e.message}"
            } catch (e: SecurityException) {
                failures += "${target.url}: ${e.message}"
            }
        }
        error.text = failures.joinToString("\n")
        error.visibility = View.VISIBLE
    }

    // The flags overload is the only one before API 33.
    @Suppress("DEPRECATION")
    private fun appUpdatedAt(): Long =
        activity.packageManager.getPackageInfo(activity.packageName, 0).lastUpdateTime
}

/**
 * Runs `aether update --check --json` with the gateway's environment and
 * returns the line it printed, or null when it failed, was offline, timed
 * out, or was interrupted. Blocks; never call it on the main thread.
 */
private fun runCheck(context: Context): String? {
    val bin = File(context.applicationInfo.nativeLibraryDir, "libaether.so")
    // Its own file: the gateway rewrites resolv.conf while it runs.
    val resolv = File(context.filesDir, "update-resolv.conf")
    var child: Process? = null
    return try {
        val connectivity = context.getSystemService(ConnectivityManager::class.java)
        val servers =
            connectivity.getLinkProperties(connectivity.activeNetwork)?.dnsServers?.mapNotNull { it.hostAddress }
                ?: return null
        resolv.writeText(resolvConf(servers))
        val builder =
            ProcessBuilder(bin.absolutePath, "update", "--check", "--json").redirectErrorStream(true)
        builder.environment().putAll(cliEnvironment(context, resolv))
        val process = builder.start()
        child = process
        process.outputStream.close()
        if (!process.waitFor(CHECK_TIMEOUT_SECONDS, TimeUnit.SECONDS)) return null
        process.inputStream.bufferedReader().readLines().firstOrNull { parseUpdateCheck(it) != null }
    } catch (_: IOException) {
        null
    } catch (_: InterruptedException) {
        null
    } finally {
        child?.destroyForcibly()
    }
}
