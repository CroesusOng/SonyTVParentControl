package dev.homelab.braviaguard.enforce

import android.accessibilityservice.AccessibilityService
import android.graphics.Color
import android.graphics.PixelFormat
import android.os.Handler
import android.os.Looper
import android.view.Gravity
import android.view.KeyEvent
import android.view.View
import android.view.WindowManager
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.TextView
import dev.homelab.braviaguard.R

/**
 * The full-screen "this app is blocked" layer.
 *
 * Added as a TYPE_ACCESSIBILITY_OVERLAY window, which is the one overlay type an
 * accessibility service may add with no further permission and which draws above other
 * apps. Starting an Activity instead would run into Android 10+ background-activity-launch
 * restrictions; the overlay has no such problem.
 *
 * Plain Views rather than Compose, deliberately: hosting Compose in a WindowManager window
 * means supplying the ViewTree lifecycle/saved-state owners by hand, and a silent failure
 * there would mean a blank screen at exactly the moment enforcement is supposed to be
 * visible. The Compose-for-TV surface is the parent dashboard, where an Activity hosts it
 * normally.
 */
class BlockOverlay(private val service: AccessibilityService) {

    private val handler = Handler(Looper.getMainLooper())
    private val windowManager =
        service.getSystemService(WindowManager::class.java)

    private var root: FrameLayout? = null
    private var statusView: TextView? = null
    private var picker: DigitPickerView? = null

    var onCodeSubmitted: ((String) -> Unit)? = null
    var onDismissed: (() -> Unit)? = null

    @Volatile
    var isVisible: Boolean = false
        private set

    fun show(presentation: BlockPresentation) = handler.post {
        if (root != null) {
            hideNow()
        }
        val view = build(presentation)
        val params = WindowManager.LayoutParams(
            WindowManager.LayoutParams.MATCH_PARENT,
            WindowManager.LayoutParams.MATCH_PARENT,
            WindowManager.LayoutParams.TYPE_ACCESSIBILITY_OVERLAY,
            // Focusable on purpose: the overlay must receive the D-pad so the code entry
            // works and so the blocked app underneath stops receiving keys.
            WindowManager.LayoutParams.FLAG_LAYOUT_IN_SCREEN or
                WindowManager.LayoutParams.FLAG_LAYOUT_NO_LIMITS or
                WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON,
            PixelFormat.TRANSLUCENT,
        ).apply { gravity = Gravity.TOP or Gravity.START }

        runCatching { windowManager.addView(view, params) }
            .onSuccess {
                root = view
                isVisible = true
                view.requestFocus()
                picker?.requestFocus()
                scheduleAutoDismiss()
            }
    }

    fun hide() = handler.post { hideNow() }

    fun showCodeResult(message: String, success: Boolean) = handler.post {
        statusView?.apply {
            text = message
            setTextColor(if (success) OK_COLOR else ERROR_COLOR)
            visibility = View.VISIBLE
        }
        if (success) {
            handler.postDelayed({ hideNow() }, SUCCESS_LINGER_MS)
        } else {
            picker?.reset()
            scheduleAutoDismiss()
        }
    }

    private fun hideNow() {
        val view = root ?: return
        runCatching { windowManager.removeView(view) }
        root = null
        statusView = null
        picker = null
        isVisible = false
        handler.removeCallbacks(autoDismiss)
    }

    private val autoDismiss = Runnable {
        hideNow()
        onDismissed?.invoke()
    }

    /**
     * The overlay is a message, not a permanent shield — the shield is kicking the blocked
     * app back to the home screen, which happens every time it comes forward. Without this
     * timeout a TV nobody is watching would sit under a black panel indefinitely.
     */
    private fun scheduleAutoDismiss() {
        handler.removeCallbacks(autoDismiss)
        handler.postDelayed(autoDismiss, AUTO_DISMISS_MS)
    }

    private fun build(presentation: BlockPresentation): FrameLayout {
        val context = service
        val column = LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            // 10-foot UI: generous margins so nothing lands in a TV's overscan area.
            setPadding(dpi(96), dpi(72), dpi(96), dpi(72))
        }

        column.addView(label(presentation.appLabel, 22f, ACCENT_COLOR))
        column.addView(label(context.getString(R.string.block_title), 40f, Color.WHITE, topDp = 8))
        column.addView(label(presentation.reason, 24f, BODY_COLOR, topDp = 20))
        presentation.retry?.let { column.addView(label(it, 22f, BODY_COLOR, topDp = 8)) }
        column.addView(label(presentation.hint, 20f, MUTED_COLOR, topDp = 24))

        column.addView(label(context.getString(R.string.block_code_label), 18f, MUTED_COLOR, topDp = 32))
        val entry = DigitPickerView(context) { code -> onCodeSubmitted?.invoke(code) }.apply {
            val params = LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.WRAP_CONTENT,
                LinearLayout.LayoutParams.WRAP_CONTENT,
            ).apply { topMargin = dpi(12) }
            layoutParams = params
        }
        picker = entry
        column.addView(entry)

        statusView = label("", 20f, ERROR_COLOR, topDp = 16).apply { visibility = View.GONE }
        column.addView(statusView)
        column.addView(label(context.getString(R.string.block_code_help), 16f, MUTED_COLOR, topDp = 16))
        column.addView(label(context.getString(R.string.block_dismiss_hint), 16f, MUTED_COLOR, topDp = 4))

        return object : FrameLayout(context) {
            override fun dispatchKeyEvent(event: KeyEvent): Boolean {
                if (event.action == KeyEvent.ACTION_UP &&
                    (event.keyCode == KeyEvent.KEYCODE_BACK || event.keyCode == KeyEvent.KEYCODE_ESCAPE)
                ) {
                    hideNow()
                    onDismissed?.invoke()
                    return true
                }
                return super.dispatchKeyEvent(event)
            }
        }.apply {
            setBackgroundColor(SCRIM_COLOR)
            isFocusable = true
            isFocusableInTouchMode = true
            addView(
                column,
                FrameLayout.LayoutParams(
                    FrameLayout.LayoutParams.MATCH_PARENT,
                    FrameLayout.LayoutParams.WRAP_CONTENT,
                    Gravity.CENTER_VERTICAL,
                ),
            )
        }
    }

    private fun label(text: String, sizeSp: Float, color: Int, topDp: Int = 0): TextView =
        TextView(service).apply {
            this.text = text
            textSize = sizeSp
            setTextColor(color)
            gravity = Gravity.CENTER_HORIZONTAL
            layoutParams = LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT,
            ).apply { topMargin = dpi(topDp) }
        }

    private fun dpi(value: Int) =
        (value * service.resources.displayMetrics.density).toInt()

    private companion object {
        const val AUTO_DISMISS_MS = 90_000L
        const val SUCCESS_LINGER_MS = 2_000L
        const val SCRIM_COLOR = 0xF20B0F13.toInt()
        const val ACCENT_COLOR = 0xFF4FC3F7.toInt()
        const val BODY_COLOR = 0xFFE0E6EB.toInt()
        const val MUTED_COLOR = 0xFF8FA3B0.toInt()
        const val ERROR_COLOR = 0xFFE57373.toInt()
        const val OK_COLOR = 0xFF81C784.toInt()
    }
}
