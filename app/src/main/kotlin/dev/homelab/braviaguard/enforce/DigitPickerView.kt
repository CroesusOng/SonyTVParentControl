package dev.homelab.braviaguard.enforce

import android.annotation.SuppressLint
import android.content.Context
import android.graphics.Color
import android.graphics.drawable.GradientDrawable
import android.view.Gravity
import android.view.KeyEvent
import android.widget.LinearLayout
import android.widget.TextView

/**
 * A six-digit entry driven entirely by the D-pad.
 *
 * An accessibility overlay cannot rely on the TV's on-screen keyboard appearing — IMEs
 * are not reliably shown for overlay windows — so instead of a text field this is a row
 * of digit wheels: up/down changes the digit under the cursor, left/right moves it, OK
 * submits. It needs nothing from the system but key events.
 */
@SuppressLint("ViewConstructor") // Built in code for the overlay; never inflated from XML.
class DigitPickerView(
    context: Context,
    private val length: Int = 6,
    private val onSubmit: (String) -> Unit,
) : LinearLayout(context) {

    private val digits = IntArray(length)
    private val cells = mutableListOf<TextView>()
    private var cursor = 0

    init {
        orientation = HORIZONTAL
        gravity = Gravity.CENTER
        isFocusable = true
        isFocusableInTouchMode = true

        repeat(length) { index ->
            val cell = TextView(context).apply {
                textSize = 40f
                gravity = Gravity.CENTER
                setTextColor(Color.WHITE)
                val size = dp(72)
                layoutParams = LayoutParams(size, dp(96)).apply {
                    marginEnd = if (index == length - 1) 0 else dp(12)
                }
            }
            cells += cell
            addView(cell)
        }
        render()
    }

    fun value(): String = digits.joinToString("")

    fun reset() {
        digits.fill(0)
        cursor = 0
        render()
    }

    override fun onKeyDown(keyCode: Int, event: KeyEvent): Boolean = when (keyCode) {
        KeyEvent.KEYCODE_DPAD_UP -> {
            digits[cursor] = (digits[cursor] + 1).mod(10)
            render(); true
        }

        KeyEvent.KEYCODE_DPAD_DOWN -> {
            digits[cursor] = (digits[cursor] - 1).mod(10)
            render(); true
        }

        KeyEvent.KEYCODE_DPAD_LEFT -> {
            if (cursor > 0) { cursor--; render() }
            true
        }

        KeyEvent.KEYCODE_DPAD_RIGHT -> {
            if (cursor < length - 1) { cursor++; render() }
            true
        }

        KeyEvent.KEYCODE_DPAD_CENTER, KeyEvent.KEYCODE_ENTER, KeyEvent.KEYCODE_NUMPAD_ENTER -> {
            onSubmit(value()); true
        }

        // Some remotes have a number pad; honour it and advance like a normal field.
        in KeyEvent.KEYCODE_0..KeyEvent.KEYCODE_9 -> {
            digits[cursor] = keyCode - KeyEvent.KEYCODE_0
            if (cursor < length - 1) cursor++
            render(); true
        }

        else -> super.onKeyDown(keyCode, event)
    }

    override fun onFocusChanged(gainFocus: Boolean, direction: Int, previously: android.graphics.Rect?) {
        super.onFocusChanged(gainFocus, direction, previously)
        render()
    }

    private fun render() {
        cells.forEachIndexed { index, cell ->
            // ASCII digits deliberately, not locale-formatted ones: this has to read back
            // as the same six characters the TOTP check compares against.
            @SuppressLint("SetTextI18n")
            cell.text = digits[index].toString()
            val focused = index == cursor && hasFocus()
            cell.background = GradientDrawable().apply {
                cornerRadius = dp(10).toFloat()
                setColor(if (focused) FOCUSED_FILL else IDLE_FILL)
                setStroke(dp(3), if (focused) ACCENT else IDLE_STROKE)
            }
            cell.setTextColor(if (focused) Color.WHITE else IDLE_TEXT)
        }
    }

    private fun dp(value: Int) = (value * resources.displayMetrics.density).toInt()

    private companion object {
        const val ACCENT = 0xFF4FC3F7.toInt()
        const val FOCUSED_FILL = 0xFF1E2A34.toInt()
        const val IDLE_FILL = 0xFF161C22.toInt()
        const val IDLE_STROKE = 0xFF2E3A44.toInt()
        const val IDLE_TEXT = 0xFFB0BEC5.toInt()
    }
}
