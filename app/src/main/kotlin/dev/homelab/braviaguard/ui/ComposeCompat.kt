package dev.homelab.braviaguard.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.State
import androidx.compose.runtime.collectAsState
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.flow.StateFlow

/** Thin aliases so the screens read as prose rather than as framework calls. */
@Composable
fun <T> StateFlow<T>.collectAsStateCompat(): State<T> = collectAsState()

@Composable
fun stringRes(id: Int): String = stringResource(id)

@Composable
fun stringRes(id: Int, vararg args: Any): String = stringResource(id, *args)
