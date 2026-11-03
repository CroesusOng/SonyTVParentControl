package dev.homelab.braviaguard.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsFocusedAsState
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.tv.material3.Text

/** A titled block. Everything on the dashboard is one of these. */
@Composable
fun Section(
    title: String,
    modifier: Modifier = Modifier,
    accent: Color = BorderIdle,
    content: @Composable ColumnScope.() -> Unit,
) {
    Column(
        modifier
            .fillMaxWidth()
            .background(SurfaceRaised, RoundedCornerShape(16.dp))
            .border(2.dp, accent, RoundedCornerShape(16.dp))
            .padding(horizontal = 28.dp, vertical = 22.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Text(
            text = title,
            fontSize = 15.sp,
            letterSpacing = 1.5.sp,
            fontWeight = FontWeight.SemiBold,
            color = TextMuted,
        )
        content()
    }
}

@Composable
fun StatLine(label: String, value: String, valueColor: Color = TextPrimary) {
    Row(
        Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.SpaceBetween,
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(label, fontSize = 20.sp, color = TextMuted)
        Text(value, fontSize = 22.sp, color = valueColor, fontWeight = FontWeight.Medium)
    }
}

@Composable
fun Body(text: String, color: Color = TextPrimary, size: Int = 20) {
    Text(text, fontSize = size.sp, color = color, lineHeight = (size * 1.4).sp)
}

/**
 * A text field sized for a remote control. There is no TV-flavoured text field in
 * tv-material3, so this is foundation's BasicTextField with a focus ring big enough to
 * find from the sofa — focus has to be obvious when you cannot see a cursor from 3 metres.
 */
@Composable
fun TvTextField(
    value: String,
    onValueChange: (String) -> Unit,
    label: String,
    modifier: Modifier = Modifier,
    placeholder: String = "",
    keyboardType: KeyboardType = KeyboardType.Text,
) {
    val interactionSource = remember { MutableInteractionSource() }
    val focused by interactionSource.collectIsFocusedAsState()

    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(label, fontSize = 15.sp, color = if (focused) Accent else TextMuted)
        BasicTextField(
            value = value,
            onValueChange = onValueChange,
            singleLine = true,
            textStyle = TextStyle(color = TextPrimary, fontSize = 24.sp),
            cursorBrush = SolidColor(Accent),
            interactionSource = interactionSource,
            keyboardOptions = KeyboardOptions(keyboardType = keyboardType),
            modifier = Modifier
                .fillMaxWidth()
                .background(Color(0xFF0D1319), RoundedCornerShape(10.dp))
                .border(
                    width = if (focused) 3.dp else 2.dp,
                    color = if (focused) Accent else BorderIdle,
                    shape = RoundedCornerShape(10.dp),
                )
                .padding(horizontal = 18.dp, vertical = 14.dp),
            decorationBox = { inner ->
                if (value.isEmpty() && placeholder.isNotEmpty()) {
                    Text(placeholder, fontSize = 24.sp, color = Color(0xFF4A5A66))
                }
                inner()
            },
        )
    }
}
