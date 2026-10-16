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
import kotlin.random.Random

private const val CHECK_TIMEOUT_SECONDS = 30L

/**
 * The banner that says a newer Aether release is out, in either mode. The
 * bundled CLI's `update --check --json` decides that, so the release lookup
 * and version ordering are the desktop's. Its button opens Google Play or
 * the release APK in the browser; this app downloads and installs nothing.
 * Once the last answer is a day old, a failed check shows as one line.
 */
class UpdateNotice(private val activity: Activity, private val banner: View) {
    private val main = Handler(Looper.getMainLooper())
    private val executor: ExecutorService = Executors.newSingleThreadExecutor()
    private var facts: InstallFacts? = null
    private var check: Future<*>? = null
    private var destroyed = false
    private var shown = ""

    private val title = banner.findViewById<TextView>(R.id.update_title)
    private val running = banner.findViewById<TextView>(R.id.update_running)
    private val detail = banner.findViewById<TextView>(R.id.update_detail)
    private val error = banner.findViewById<TextView>(R.id.update_error)
    private val failed = banner.findViewById<TextView>(R.id.update_check_failed)
    private val offer = banner.findViewById<View>(R.id.update_offer)
    private val action = banner.findViewById<Button>(R.id.update_action)
    private val notes = banner.findViewById<Button>(R.id.update_notes)

    init {
        banner.findViewById<View>(R.id.update_dismiss).setOnClickListener {
            val now = System.currentTimeMillis()
            val stored = activity.storedUpdate()
            activity.storeUpdate(
                stored.copy(
                    dismissed = if (offer.visibility == View.VISIBLE) shown else stored.dismissed,
                    failureDismissedAt = if (failed.visibility == View.VISIBLE) now else stored.failureDismissedAt,
                ),
            )
            banner.visibility = View.GONE
        }
        action.setOnClickListener {
            val source = facts?.source ?: return@setOnClickListener
            openUpdate(activity, updateTargets(source, activity.packageName, shown).orEmpty(), error)
        }
        notes.setOnClickListener {
            openUpdate(activity, listOfNotNull(releaseNotesUrl(shown)?.let(::UpdateTarget)), error)
        }
    }

    fun resume() {
        val stored = activity.storedUpdate()
        render(stored)
        if (check?.isDone == false) return
        val context = activity.applicationContext
        check =
            executor.submit {
                val read = installFacts(context)
                main.post {
                    if (destroyed) return@post
                    facts = read
                    render(activity.storedUpdate())
                }
                if (checkDue(System.currentTimeMillis(), stored, read.updatedAt)) {
                    val run = runCheck(context)
                    main.post {
                        if (destroyed) return@post
                        val next = recordCheck(activity, run, read.updatedAt)
                        render(next)
                    }
                }
            }
    }

    fun destroy() {
        destroyed = true
        check?.cancel(true)
        executor.shutdownNow()
    }

    private fun render(stored: StoredUpdate) {
        val read = facts
        val now = System.currentTimeMillis()
        val found = read?.let { noticeFor(stored, it.source, now, it.updatedAt) }
        val failure = checkFailure(stored, now)
        if (read == null || (found == null && failure == null)) {
            banner.visibility = View.GONE
            return
        }
        failed.text = failure?.let { activity.getString(R.string.update_check_failed, it) }
        failed.visibility = if (failure != null) View.VISIBLE else View.GONE
        val offered = if (found != null) View.VISIBLE else View.GONE
        for (view in listOf(title, running, offer)) view.visibility = offered
        if (found != null) {
            if (found.latest != shown) error.visibility = View.GONE
            shown = found.latest
            val play = read.source == InstallSource.PLAY
            title.text = activity.getString(R.string.update_title, found.latest)
            running.text = activity.getString(R.string.update_running, found.version)
            detail.setText(if (play) R.string.update_play_detail else R.string.update_release_detail)
            action.setText(if (play) R.string.update_open_play else R.string.update_download)
        }
        banner.visibility = View.VISIBLE
    }
}

/** What one run of `aether update --check --json` printed: the check line, else why there is none. */
class CheckRun(val line: String?, val error: String)

/**
 * Stores [run] as the latest check and returns what was stored. Main
 * thread only: the read and the write must not interleave with another.
 */
fun recordCheck(context: Context, run: CheckRun, appUpdatedAt: Long): StoredUpdate {
    val next =
        afterCheck(
            context.storedUpdate(),
            System.currentTimeMillis(),
            run.line,
            run.error,
            appUpdatedAt,
            Random.nextDouble(-CHECK_JITTER, CHECK_JITTER),
        )
    context.storeUpdate(next)
    return next
}

/**
 * Opens the first of [targets] some app on the phone handles. When none
 * does, [error] shows each one's failure verbatim.
 */
fun openUpdate(activity: Activity, targets: List<UpdateTarget>, error: TextView) {
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

/**
 * Runs `aether update --check --json` with the gateway's environment. The
 * error is what the CLI printed, or why it could not run or finish.
 * Blocks; never call it on the main thread.
 */
fun runCheck(context: Context): CheckRun = synchronized(checkLock) { runCheckLocked(context) }

// The banner and Check for updates can run at once, and both write update-resolv.conf.
private val checkLock = Any()

private fun runCheckLocked(context: Context): CheckRun {
    val bin = File(context.applicationInfo.nativeLibraryDir, "libaether.so")
    // Its own file: the gateway rewrites resolv.conf while it runs.
    val resolv = File(context.filesDir, "update-resolv.conf")
    var child: Process? = null
    return try {
        val connectivity = context.getSystemService(ConnectivityManager::class.java)
        val servers =
            connectivity.getLinkProperties(connectivity.activeNetwork)?.dnsServers?.mapNotNull { it.hostAddress }
                ?: return CheckRun(null, "no network: Android reports no DNS servers for the active network")
        resolv.writeText(resolvConf(servers))
        val builder =
            ProcessBuilder(bin.absolutePath, "update", "--check", "--json").redirectErrorStream(true)
        builder.environment().putAll(cliEnvironment(context, resolv))
        val process = builder.start()
        child = process
        process.outputStream.close()
        if (!process.waitFor(CHECK_TIMEOUT_SECONDS, TimeUnit.SECONDS)) {
            return CheckRun(null, "aether update --check gave no answer within ${CHECK_TIMEOUT_SECONDS}s")
        }
        val lines = process.inputStream.bufferedReader().readLines()
        CheckRun(lines.firstOrNull { parseUpdateCheck(it) != null }, lines.joinToString("\n").trim())
    } catch (e: IOException) {
        CheckRun(null, "could not run ${bin.absolutePath}: ${e.message}")
    } catch (_: InterruptedException) {
        CheckRun(null, "the check was interrupted")
    } finally {
        child?.destroyForcibly()
    }
}
